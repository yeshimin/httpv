#include <ngx_config.h>
#include <ngx_core.h>
#include <ngx_http.h>

#include <sys/socket.h>
#include <sys/un.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>

#define NGX_HTTP_HTTPV_INVALID_SOCKET (-1)
#define NGX_HTTP_HTTPV_RING_VERSION 1
#define NGX_HTTP_HTTPV_RING_HEADER_SIZE 64
#define NGX_HTTP_HTTPV_RING_RECORD_SIZE 2048
#define NGX_HTTP_HTTPV_RING_CAPACITY 4096
#define NGX_HTTP_HTTPV_RING_PATH_SIZE 1024

typedef struct {
    u_char      magic[4];
    uint32_t    version;
    uint32_t    record_size;
    uint32_t    capacity;
    uint64_t    write_seq;
    uint64_t    read_seq;
    u_char      reserved[32];
} ngx_http_httpv_ring_header_t;

typedef struct {
    ngx_str_t       socket_path;
    ngx_socket_t    fd;
    struct sockaddr_un addr;
    socklen_t       addrlen;
    ngx_atomic_t    sent;
    ngx_atomic_t    dropped;
    ngx_str_t       ring_path;
    ngx_int_t       ring_fd;
    size_t          ring_size;
    u_char          ring_file[NGX_HTTP_HTTPV_RING_PATH_SIZE];
    ngx_http_httpv_ring_header_t *ring;
    ngx_atomic_t    ring_enqueued;
    ngx_atomic_t    ring_dropped;
} ngx_http_httpv_main_conf_t;

typedef struct {
    ngx_flag_t enabled;
} ngx_http_httpv_loc_conf_t;

static ngx_int_t ngx_http_httpv_postconfiguration(ngx_conf_t *cf);
static void *ngx_http_httpv_create_main_conf(ngx_conf_t *cf);
static void *ngx_http_httpv_create_loc_conf(ngx_conf_t *cf);
static char *ngx_http_httpv_merge_loc_conf(ngx_conf_t *cf, void *parent, void *child);
static char *ngx_http_httpv_set_socket(ngx_conf_t *cf, ngx_command_t *cmd, void *conf);
static char *ngx_http_httpv_set_ring(ngx_conf_t *cf, ngx_command_t *cmd, void *conf);
static char *ngx_http_httpv_metrics(ngx_conf_t *cf, ngx_command_t *cmd, void *conf);
static ngx_int_t ngx_http_httpv_init_process(ngx_cycle_t *cycle);
static void ngx_http_httpv_exit_process(ngx_cycle_t *cycle);
static ngx_int_t ngx_http_httpv_log_handler(ngx_http_request_t *r);
static ngx_int_t ngx_http_httpv_metrics_handler(ngx_http_request_t *r);
static ngx_int_t ngx_http_httpv_init_ring(ngx_cycle_t *cycle, ngx_http_httpv_main_conf_t *mcf);
static void ngx_http_httpv_close_ring(ngx_http_httpv_main_conf_t *mcf);
static ngx_int_t ngx_http_httpv_ring_write(ngx_http_httpv_main_conf_t *mcf, u_char *frame, size_t frame_len);

static ngx_str_t ngx_http_httpv_request_id_variable = ngx_string("httpv_request_id");
static ngx_str_t ngx_http_httpv_subject_id_variable = ngx_string("httpv_subject_id");
static ngx_str_t ngx_http_httpv_enabled_variable = ngx_string("httpv_native_response_telemetry");

static ngx_command_t ngx_http_httpv_commands[] = {
    {
        ngx_string("httpv_native_socket"),
        NGX_HTTP_MAIN_CONF|NGX_CONF_TAKE1,
        ngx_http_httpv_set_socket,
        NGX_HTTP_MAIN_CONF_OFFSET,
        0,
        NULL
    },
    {
        ngx_string("httpv_native_ring"),
        NGX_HTTP_MAIN_CONF|NGX_CONF_TAKE1,
        ngx_http_httpv_set_ring,
        NGX_HTTP_MAIN_CONF_OFFSET,
        0,
        NULL
    },
    {
        ngx_string("httpv_native_telemetry"),
        NGX_HTTP_SRV_CONF|NGX_HTTP_LOC_CONF|NGX_CONF_FLAG,
        ngx_conf_set_flag_slot,
        NGX_HTTP_LOC_CONF_OFFSET,
        offsetof(ngx_http_httpv_loc_conf_t, enabled),
        NULL
    },
    {
        ngx_string("httpv_native_metrics"),
        NGX_HTTP_LOC_CONF|NGX_CONF_NOARGS,
        ngx_http_httpv_metrics,
        0,
        0,
        NULL
    },
    ngx_null_command
};

static ngx_http_module_t ngx_http_httpv_module_ctx = {
    NULL,
    ngx_http_httpv_postconfiguration,
    ngx_http_httpv_create_main_conf,
    NULL,
    NULL,
    NULL,
    ngx_http_httpv_create_loc_conf,
    ngx_http_httpv_merge_loc_conf
};

ngx_module_t ngx_http_httpv_module = {
    NGX_MODULE_V1,
    &ngx_http_httpv_module_ctx,
    ngx_http_httpv_commands,
    NGX_HTTP_MODULE,
    NULL,
    NULL,
    ngx_http_httpv_init_process,
    NULL,
    NULL,
    ngx_http_httpv_exit_process,
    NULL,
    NGX_MODULE_V1_PADDING
};

static void ngx_http_httpv_put_u16(u_char **p, uint16_t value) {
    *(*p)++ = (u_char) (value >> 8);
    *(*p)++ = (u_char) value;
}

static void ngx_http_httpv_put_u32(u_char **p, uint32_t value) {
    *(*p)++ = (u_char) (value >> 24);
    *(*p)++ = (u_char) (value >> 16);
    *(*p)++ = (u_char) (value >> 8);
    *(*p)++ = (u_char) value;
}

static void ngx_http_httpv_put_u64(u_char **p, uint64_t value) {
    ngx_http_httpv_put_u32(p, (uint32_t) (value >> 32));
    ngx_http_httpv_put_u32(p, (uint32_t) value);
}

static ngx_int_t ngx_http_httpv_put_text(u_char **p, u_char *last, ngx_str_t value) {
    if ((size_t) (last - *p) < 2 || value.len > 65535 || (size_t) (last - *p - 2) < value.len) {
        return NGX_ERROR;
    }
    ngx_http_httpv_put_u16(p, (uint16_t) value.len);
    *p = ngx_copy(*p, value.data, value.len);
    return NGX_OK;
}

static ngx_int_t ngx_http_httpv_postconfiguration(ngx_conf_t *cf) {
    ngx_http_core_main_conf_t *cmcf;
    ngx_http_handler_pt *h;

    cmcf = ngx_http_conf_get_module_main_conf(cf, ngx_http_core_module);
    h = ngx_array_push(&cmcf->phases[NGX_HTTP_LOG_PHASE].handlers);
    if (h == NULL) {
        return NGX_ERROR;
    }
    *h = ngx_http_httpv_log_handler;
    return NGX_OK;
}

static void *ngx_http_httpv_create_main_conf(ngx_conf_t *cf) {
    ngx_http_httpv_main_conf_t *conf;
    conf = ngx_pcalloc(cf->pool, sizeof(ngx_http_httpv_main_conf_t));
    if (conf == NULL) {
        return NULL;
    }
    conf->fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
    conf->ring_fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
    return conf;
}

static void *ngx_http_httpv_create_loc_conf(ngx_conf_t *cf) {
    ngx_http_httpv_loc_conf_t *conf;
    conf = ngx_pcalloc(cf->pool, sizeof(ngx_http_httpv_loc_conf_t));
    if (conf == NULL) {
        return NULL;
    }
    conf->enabled = NGX_CONF_UNSET;
    return conf;
}

static char *ngx_http_httpv_merge_loc_conf(ngx_conf_t *cf, void *parent, void *child) {
    ngx_http_httpv_loc_conf_t *prev = parent;
    ngx_http_httpv_loc_conf_t *conf = child;
    ngx_conf_merge_value(conf->enabled, prev->enabled, 0);
    return NGX_CONF_OK;
}

static char *ngx_http_httpv_set_socket(ngx_conf_t *cf, ngx_command_t *cmd, void *conf) {
    ngx_http_httpv_main_conf_t *mcf = conf;
    ngx_str_t *value = cf->args->elts;
    if (mcf->socket_path.data != NULL) {
        return "is duplicate";
    }
    if (value[1].len == 0 || value[1].len >= sizeof(mcf->addr.sun_path)) {
        return "socket path is invalid or too long";
    }
    mcf->socket_path = value[1];
    return NGX_CONF_OK;
}

static char *ngx_http_httpv_set_ring(ngx_conf_t *cf, ngx_command_t *cmd, void *conf) {
    ngx_http_httpv_main_conf_t *mcf = conf;
    ngx_str_t *value = cf->args->elts;
    if (mcf->ring_path.data != NULL) {
        return "is duplicate";
    }
    if (value[1].len == 0 || value[1].len + 32 >= sizeof(mcf->ring_file)) {
        return "ring path is invalid or too long";
    }
    mcf->ring_path = value[1];
    return NGX_CONF_OK;
}

static char *ngx_http_httpv_metrics(ngx_conf_t *cf, ngx_command_t *cmd, void *conf) {
    ngx_http_core_loc_conf_t *clcf;

    clcf = ngx_http_conf_get_module_loc_conf(cf, ngx_http_core_module);
    clcf->handler = ngx_http_httpv_metrics_handler;
    return NGX_CONF_OK;
}

static ngx_int_t ngx_http_httpv_init_process(ngx_cycle_t *cycle) {
    ngx_http_conf_ctx_t *http_ctx;
    ngx_http_httpv_main_conf_t *mcf;
    int flags;

    http_ctx = (ngx_http_conf_ctx_t *) ngx_get_conf(cycle->conf_ctx, ngx_http_module);
    if (http_ctx == NULL) {
        return NGX_OK;
    }
    mcf = http_ctx->main_conf[ngx_http_httpv_module.ctx_index];
    if (mcf == NULL || mcf->socket_path.len == 0) {
        return NGX_OK;
    }

    mcf->fd = socket(AF_UNIX, SOCK_DGRAM, 0);
    if (mcf->fd == NGX_HTTP_HTTPV_INVALID_SOCKET) {
        ngx_log_error(NGX_LOG_ALERT, cycle->log, ngx_socket_errno, "httpv native socket() failed");
        return NGX_ERROR;
    }
    flags = fcntl(mcf->fd, F_GETFL, 0);
    if (flags == -1 || fcntl(mcf->fd, F_SETFL, flags | O_NONBLOCK) == -1) {
        ngx_log_error(NGX_LOG_ALERT, cycle->log, ngx_errno, "httpv native fcntl() failed");
        ngx_close_socket(mcf->fd);
        mcf->fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
        return NGX_ERROR;
    }
    ngx_memzero(&mcf->addr, sizeof(struct sockaddr_un));
    mcf->addr.sun_family = AF_UNIX;
    ngx_memcpy(mcf->addr.sun_path, mcf->socket_path.data, mcf->socket_path.len);
    mcf->addr.sun_path[mcf->socket_path.len] = '\0';
    mcf->addrlen = (socklen_t) (offsetof(struct sockaddr_un, sun_path) + mcf->socket_path.len + 1);
    if (ngx_process == NGX_PROCESS_WORKER && ngx_http_httpv_init_ring(cycle, mcf) != NGX_OK) {
        ngx_log_error(NGX_LOG_WARN, cycle->log, 0,
                      "httpv native ring unavailable; falling back to Unix datagram telemetry");
    }
    return NGX_OK;
}

static void ngx_http_httpv_exit_process(ngx_cycle_t *cycle) {
    ngx_http_conf_ctx_t *http_ctx;
    ngx_http_httpv_main_conf_t *mcf;
    http_ctx = (ngx_http_conf_ctx_t *) ngx_get_conf(cycle->conf_ctx, ngx_http_module);
    if (http_ctx == NULL) {
        return;
    }
    mcf = http_ctx->main_conf[ngx_http_httpv_module.ctx_index];
    if (mcf) {
        ngx_http_httpv_close_ring(mcf);
        if (mcf->fd != NGX_HTTP_HTTPV_INVALID_SOCKET) {
            ngx_close_socket(mcf->fd);
            mcf->fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
        }
    }
}

static ngx_int_t ngx_http_httpv_init_ring(ngx_cycle_t *cycle, ngx_http_httpv_main_conf_t *mcf) {
    u_char *path_end;
    size_t expected_size;
    void *mapping;

    if (mcf->ring_path.len == 0 || ngx_process != NGX_PROCESS_WORKER) {
        return NGX_DECLINED;
    }
    path_end = ngx_snprintf(mcf->ring_file, sizeof(mcf->ring_file), "%V.%P", &mcf->ring_path, ngx_pid);
    if (path_end >= mcf->ring_file + sizeof(mcf->ring_file) - 1) {
        return NGX_ERROR;
    }
    *path_end = '\0';
    expected_size = NGX_HTTP_HTTPV_RING_HEADER_SIZE
        + (NGX_HTTP_HTTPV_RING_RECORD_SIZE * NGX_HTTP_HTTPV_RING_CAPACITY);
    mcf->ring_fd = open((const char *) mcf->ring_file, O_RDWR | O_CREAT, 0666);
    if (mcf->ring_fd == NGX_HTTP_HTTPV_INVALID_SOCKET) {
        ngx_log_error(NGX_LOG_WARN, cycle->log, ngx_errno, "httpv native ring open() failed");
        return NGX_ERROR;
    }
    if (ftruncate(mcf->ring_fd, (off_t) expected_size) == -1) {
        ngx_log_error(NGX_LOG_WARN, cycle->log, ngx_errno, "httpv native ring ftruncate() failed");
        ngx_close_file(mcf->ring_fd);
        mcf->ring_fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
        return NGX_ERROR;
    }
    mapping = mmap(NULL, expected_size, PROT_READ | PROT_WRITE, MAP_SHARED, mcf->ring_fd, 0);
    if (mapping == MAP_FAILED) {
        ngx_log_error(NGX_LOG_WARN, cycle->log, ngx_errno, "httpv native ring mmap() failed");
        ngx_close_file(mcf->ring_fd);
        mcf->ring_fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
        return NGX_ERROR;
    }
    mcf->ring = mapping;
    mcf->ring_size = expected_size;
    if (ngx_memcmp(mcf->ring->magic, "HTVR", 4) != 0
        || mcf->ring->version != NGX_HTTP_HTTPV_RING_VERSION
        || mcf->ring->record_size != NGX_HTTP_HTTPV_RING_RECORD_SIZE
        || mcf->ring->capacity != NGX_HTTP_HTTPV_RING_CAPACITY) {
        ngx_memzero(mcf->ring, expected_size);
        ngx_memcpy(mcf->ring->magic, "HTVR", 4);
        mcf->ring->version = NGX_HTTP_HTTPV_RING_VERSION;
        mcf->ring->record_size = NGX_HTTP_HTTPV_RING_RECORD_SIZE;
        mcf->ring->capacity = NGX_HTTP_HTTPV_RING_CAPACITY;
        __atomic_thread_fence(__ATOMIC_RELEASE);
    }
    return NGX_OK;
}

static void ngx_http_httpv_close_ring(ngx_http_httpv_main_conf_t *mcf) {
    if (mcf->ring != NULL) {
        (void) munmap(mcf->ring, mcf->ring_size);
        mcf->ring = NULL;
        mcf->ring_size = 0;
    }
    if (mcf->ring_fd != NGX_HTTP_HTTPV_INVALID_SOCKET) {
        ngx_close_file(mcf->ring_fd);
        mcf->ring_fd = NGX_HTTP_HTTPV_INVALID_SOCKET;
    }
}

static ngx_int_t ngx_http_httpv_ring_write(ngx_http_httpv_main_conf_t *mcf, u_char *frame, size_t frame_len) {
    uint64_t write_seq;
    uint64_t read_seq;
    u_char *record;

    if (mcf->ring == NULL || frame_len > NGX_HTTP_HTTPV_RING_RECORD_SIZE - sizeof(uint32_t)) {
        return NGX_DECLINED;
    }
    write_seq = __atomic_load_n(&mcf->ring->write_seq, __ATOMIC_RELAXED);
    read_seq = __atomic_load_n(&mcf->ring->read_seq, __ATOMIC_ACQUIRE);
    if (write_seq - read_seq >= mcf->ring->capacity) {
        (void) ngx_atomic_fetch_add(&mcf->ring_dropped, 1);
        (void) ngx_atomic_fetch_add(&mcf->dropped, 1);
        return NGX_BUSY;
    }
    record = (u_char *) mcf->ring + NGX_HTTP_HTTPV_RING_HEADER_SIZE
        + ((write_seq % mcf->ring->capacity) * mcf->ring->record_size);
    ngx_memcpy(record + sizeof(uint32_t), frame, frame_len);
    *((uint32_t *) record) = (uint32_t) frame_len;
    __atomic_store_n(&mcf->ring->write_seq, write_seq + 1, __ATOMIC_RELEASE);
    (void) ngx_atomic_fetch_add(&mcf->ring_enqueued, 1);
    return NGX_OK;
}

static ngx_int_t ngx_http_httpv_metrics_handler(ngx_http_request_t *r) {
    ngx_http_httpv_main_conf_t *mcf;
    ngx_buf_t *buffer;
    ngx_chain_t output;
    u_char *p;
    ngx_int_t rc;

    if (r->method != NGX_HTTP_GET && r->method != NGX_HTTP_HEAD) {
        return NGX_HTTP_NOT_ALLOWED;
    }
    mcf = ngx_http_get_module_main_conf(r, ngx_http_httpv_module);
    if (mcf == NULL) {
        return NGX_HTTP_INTERNAL_SERVER_ERROR;
    }
    buffer = ngx_create_temp_buf(r->pool, 512);
    if (buffer == NULL) {
        return NGX_HTTP_INTERNAL_SERVER_ERROR;
    }
    p = ngx_sprintf(buffer->pos,
                    "httpv_native_events_sent %uA\n"
                    "httpv_native_events_dropped %uA\n",
                    (ngx_atomic_uint_t) mcf->sent,
                    (ngx_atomic_uint_t) mcf->dropped);
    p = ngx_sprintf(p,
                    "httpv_native_ring_enqueued %uA\n"
                    "httpv_native_ring_dropped %uA\n",
                    (ngx_atomic_uint_t) mcf->ring_enqueued,
                    (ngx_atomic_uint_t) mcf->ring_dropped);
    buffer->last = p;
    buffer->last_buf = 1;
    r->headers_out.status = NGX_HTTP_OK;
    r->headers_out.content_type.len = sizeof("text/plain; version=0.0.4") - 1;
    r->headers_out.content_type.data = (u_char *) "text/plain; version=0.0.4";
    r->headers_out.content_length_n = buffer->last - buffer->pos;
    rc = ngx_http_send_header(r);
    if (rc == NGX_ERROR || rc > NGX_OK || r->header_only) {
        return rc;
    }
    output.buf = buffer;
    output.next = NULL;
    return ngx_http_output_filter(r, &output);
}

static ngx_int_t ngx_http_httpv_log_handler(ngx_http_request_t *r) {
    ngx_http_httpv_loc_conf_t *lcf;
    ngx_http_httpv_main_conf_t *mcf;
    ngx_time_t *tp;
    ngx_http_variable_value_t *variable;
    ngx_str_t request_id, subject_id, action;
    u_char request_id_buf[64];
    u_char frame[2048];
    u_char *p, *last;
    uint64_t timestamp, duration;
    uint16_t status;
    ssize_t sent;

    lcf = ngx_http_get_module_loc_conf(r, ngx_http_httpv_module);
    if (lcf == NULL || !lcf->enabled) {
        return NGX_OK;
    }
    variable = ngx_http_get_variable(r, &ngx_http_httpv_enabled_variable,
                                    ngx_hash_key(ngx_http_httpv_enabled_variable.data,
                                                 ngx_http_httpv_enabled_variable.len));
    if (variable == NULL || variable->not_found || variable->len != 1 || variable->data[0] != '1') {
        return NGX_OK;
    }
    mcf = ngx_http_get_module_main_conf(r, ngx_http_httpv_module);
    if (mcf == NULL || mcf->fd == NGX_HTTP_HTTPV_INVALID_SOCKET) {
        return NGX_OK;
    }

    variable = ngx_http_get_variable(r, &ngx_http_httpv_request_id_variable,
                                    ngx_hash_key(ngx_http_httpv_request_id_variable.data,
                                                 ngx_http_httpv_request_id_variable.len));
    if (variable != NULL && !variable->not_found && variable->len > 0) {
        request_id.data = variable->data;
        request_id.len = variable->len;
    } else {
        p = request_id_buf;
        p = ngx_sprintf(p, "%uL-%M", (uint64_t) r->connection->number, r->start_msec);
        request_id.data = request_id_buf;
        request_id.len = (size_t) (p - request_id_buf);
    }
    variable = ngx_http_get_variable(r, &ngx_http_httpv_subject_id_variable,
                                    ngx_hash_key(ngx_http_httpv_subject_id_variable.data,
                                                 ngx_http_httpv_subject_id_variable.len));
    if (variable != NULL && !variable->not_found && variable->len > 0) {
        subject_id.data = variable->data;
        subject_id.len = variable->len;
    } else {
        subject_id.len = 0;
        subject_id.data = (u_char *) "";
    }
    action.len = 0;
    action.data = (u_char *) "";
    tp = ngx_timeofday();
    timestamp = ((uint64_t) tp->sec * 1000) + tp->msec;
    duration = ngx_current_msec - r->start_msec;
    status = (uint16_t) (r->headers_out.status ? r->headers_out.status : r->err_status);

    p = frame;
    last = frame + sizeof(frame);
    if ((size_t) (last - p) < 38) {
        return NGX_OK;
    }
    *p++ = 'H'; *p++ = 'T'; *p++ = 'V'; *p++ = 'P';
    *p++ = 1;
    *p++ = 7;
    ngx_http_httpv_put_u16(&p, 0);
    ngx_http_httpv_put_u64(&p, timestamp);
    ngx_http_httpv_put_u64(&p, r->request_length);
    ngx_http_httpv_put_u64(&p, 0);
    ngx_http_httpv_put_u16(&p, status);
    ngx_http_httpv_put_u32(&p, (uint32_t) duration);
    if (ngx_http_httpv_put_text(&p, last, request_id) != NGX_OK
        || ngx_http_httpv_put_text(&p, last, subject_id) != NGX_OK
        || ngx_http_httpv_put_text(&p, last, r->method_name) != NGX_OK
        || ngx_http_httpv_put_text(&p, last, r->uri) != NGX_OK
        || ngx_http_httpv_put_text(&p, last, r->connection->addr_text) != NGX_OK
        || ngx_http_httpv_put_text(&p, last, action) != NGX_OK) {
        (void) ngx_atomic_fetch_add(&mcf->dropped, 1);
        return NGX_OK;
    }
    if (mcf->ring != NULL) {
        (void) ngx_http_httpv_ring_write(mcf, frame, (size_t) (p - frame));
        return NGX_OK;
    }
    sent = sendto(mcf->fd, frame, (size_t) (p - frame), MSG_DONTWAIT,
                  (struct sockaddr *) &mcf->addr, mcf->addrlen);
    if (sent == -1) {
        (void) ngx_atomic_fetch_add(&mcf->dropped, 1);
    } else {
        (void) ngx_atomic_fetch_add(&mcf->sent, 1);
    }
    return NGX_OK;
}
