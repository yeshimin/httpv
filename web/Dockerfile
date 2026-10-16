FROM node:25-alpine AS build
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM openresty/openresty:alpine-fat
COPY nginx.conf /usr/local/openresty/nginx/conf/nginx.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
