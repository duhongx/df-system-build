package repository

import (
	"df-build-server/internal/model"
	"df-build-server/pkg/logger"
)

// SeedCoreConfigItems seeds config items that the system NEEDS to function.
// Only inserts if the code doesn't already exist (idempotent).
func SeedCoreConfigItems() {
	items := []model.ConfigItem{
		{
			Name:        "Java Dockerfile",
			Code:        "dockerfile-java",
			Category:    "dockerfile",
			ContentType: "text",
			Description: "Java Docker image build template",
			Content: `FROM ${registryUrl}/base/java:v1
MAINTAINER Du Hong <duhong@df-mic.com>

COPY ${artifactName} /opt/app.jar
COPY app.sh /opt/app.sh
COPY delete_app.sh /opt/delete_app.sh

RUN chmod +x /opt/app.sh /opt/delete_app.sh

EXPOSE 8080

ENTRYPOINT ["/opt/app.sh"]
`,
		},
		{
			Name:        "Web Dockerfile",
			Code:        "dockerfile-web",
			Category:    "dockerfile",
			ContentType: "text",
			Description: "Vue Docker image build template",
			Content: `FROM ${registryUrl}/base/nginx:v1
MAINTAINER Du Hong <duhong@df-mic.com>

COPY dist /usr/share/nginx/html

EXPOSE 80
`,
		},
		{
			Name:        "Java Deployment",
			Code:        "deployment-java",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "Java K8s Deployment template",
			Content: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${appName}
  namespace: ${namespace}
  labels:
    app: ${appName}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${appName}
  strategy:
    rollingUpdate:
      maxSurge: 25%
      maxUnavailable: 25%
    type: RollingUpdate
  template:
    metadata:
      labels:
        app: ${appName}
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8080"
        prometheus.io/path: "/prometheus"
    spec:
      terminationGracePeriodSeconds: 60
      containers:
      - name: ${appName}
        image: ${imageName}
        imagePullPolicy: IfNotPresent
        env:
        - name: NACOS_USER
          value: nacos
        - name: NACOS_PASS
          value: cloudhis2123
        - name: APP_SERVICENAME
          value: "${appName}"
        - name: NACOS_NAMESPACE_ID
          value: "4412278f-6d05-4468-9f69-8f7a383d6697"
        - name: MY_POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
        lifecycle:
          preStop:
            exec:
              command:
              - /bin/bash
              - -c
              - /opt/delete_app.sh
        resources:
          requests:
            memory: 2.0G
            cpu: 10m
          limits:
            memory: 2.5G
        volumeMounts:
        - name: host-time
          mountPath: /etc/localtime
          readOnly: true
        ports:
        - name: http
          containerPort: 8080
          protocol: TCP
        startupProbe:
          httpGet:
            path: /actuator/health
            port: 8080
            scheme: HTTP
          initialDelaySeconds: 60
          periodSeconds: 10
          failureThreshold: 30
          timeoutSeconds: 5
        readinessProbe:
          httpGet:
            path: /actuator/health
            port: 8080
            scheme: HTTP
          periodSeconds: 10
          failureThreshold: 3
          timeoutSeconds: 5
        livenessProbe:
          httpGet:
            path: /actuator/health
            port: 8080
            scheme: HTTP
          periodSeconds: 15
          failureThreshold: 4
          timeoutSeconds: 5
      volumes:
      - name: host-time
        hostPath:
          path: /etc/localtime
      imagePullSecrets:
      - name: nexus-registry
`,
		},
		{
			Name:        "Web Deployment",
			Code:        "deployment-web",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "Vue K8s Deployment template",
			Content: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${appName}
  namespace: ${namespace}
  labels:
    app: ${appName}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${appName}
  strategy:
    rollingUpdate:
      maxSurge: 25%
      maxUnavailable: 25%
    type: RollingUpdate
  template:
    metadata:
      labels:
        app: ${appName}
    spec:
      terminationGracePeriodSeconds: 30
      containers:
      - name: ${appName}
        image: ${imageName}
        imagePullPolicy: IfNotPresent
        lifecycle:
          preStop:
            exec:
              command: ["/usr/sbin/nginx", "-s", "quit"]
        resources:
          requests:
            memory: 256Mi
            cpu: 10m
          limits:
            memory: 512Mi
        volumeMounts:
        - name: nginx-conf-volume
          mountPath: /etc/nginx/conf.d/
          readOnly: true
        - name: host-time
          mountPath: /etc/localtime
          readOnly: true
        ports:
        - name: http
          containerPort: 80
          protocol: TCP
        readinessProbe:
          httpGet:
            path: /index.html
            port: 80
            scheme: HTTP
          initialDelaySeconds: 30
          periodSeconds: 3
          failureThreshold: 3
          timeoutSeconds: 1
        livenessProbe:
          httpGet:
            path: /index.html
            port: 80
            scheme: HTTP
          initialDelaySeconds: 30
          periodSeconds: 15
          failureThreshold: 4
          timeoutSeconds: 3
      volumes:
      - name: nginx-conf-volume
        configMap:
          name: ${appName}
          defaultMode: 420
      - name: host-time
        hostPath:
          path: /etc/localtime
      imagePullSecrets:
      - name: nexus-registry
`,
		},
		{
			Name:        "Java Service",
			Code:        "service-java",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "Java K8s Service template",
			Content: `apiVersion: v1
kind: Service
metadata:
  name: ${appName}
  namespace: ${namespace}
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "8080"
    prometheus.io/path: "/actuator/prometheus"
spec:
  type: NodePort
  ports:
  - port: 8080
    targetPort: 8080
    nodePort: ${nodePort}
    protocol: TCP
  selector:
    app: ${appName}
`,
		},
		{
			Name:        "Web Service",
			Code:        "service-web",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "Vue K8s Service template",
			Content: `apiVersion: v1
kind: Service
metadata:
  name: ${appName}
  namespace: ${namespace}
spec:
  type: NodePort
  ports:
  - port: 80
    targetPort: 80
    nodePort: ${nodePort}
    protocol: TCP
  selector:
    app: ${appName}
`,
		},
		{
			Name:        "Ingress",
			Code:        "ingress",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "K8s Ingress template",
			Content: `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${appName}
  namespace: ${namespace}
  annotations:
    nginx.ingress.kubernetes.io/proxy-body-size: "100m"
    nginx.ingress.kubernetes.io/proxy-connect-timeout: "60"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "120"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "60"
spec:
  ingressClassName: nginx
  rules:
  - host: ${ingressHost}
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: ${appName}
            port:
              number: ${servicePort}
`,
		},
	}

	for _, item := range items {
		var existing model.ConfigItem
		if DB.Where("code = ?", item.Code).First(&existing).Error != nil {
			DB.Create(&item)
		}
	}

	logger.Log.Info("Core config items seeding completed")
}

// SeedPersonalConfigItems seeds personal/environment-specific config items.
// Only runs on first deployment.
func SeedPersonalConfigItems() {
	items := []model.ConfigItem{
		{
			Name:        "ConfigMap (web-main)",
			Code:        "configmap-web-main",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "web-main Nginx ConfigMap",
			Content: `apiVersion: v1
kind: ConfigMap
metadata:
  name: web-main
  namespace: ${namespace}
data:
  web-main.conf: |
    server {
        listen 80 default_server;
        listen [::]:80 default_server;
        server_name _;
        root /usr/share/nginx/html;

        # =========================
        # Activiti editor 代理
        # =========================
        location = /api/act {
            return 308 /api/act/$is_args$args;
        }

        location ^~ /api/act/ {
            proxy_pass http://${nodeIP}:9018/;
            proxy_connect_timeout 60s;
            proxy_send_timeout    600s;
            proxy_read_timeout    600s;
            send_timeout          600s;
            proxy_request_buffering off;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host               $http_host;
            proxy_set_header X-Real-IP          $remote_addr;
            proxy_set_header X-Real-Port        $remote_port;
            proxy_set_header X-Forwarded-For    $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto  $scheme;
            proxy_set_header X-Forwarded-Host   $http_host;
            proxy_set_header X-Forwarded-Prefix /api/act;
            proxy_set_header X-Original-URI     $request_uri;
        }

        # =========================
        # 普通 API 代理
        # =========================
        location = /api {
            return 308 /api/$is_args$args;
        }

        location ^~ /api/ {
            proxy_pass http://his-gateway:8080/;
            proxy_connect_timeout 60s;
            proxy_send_timeout    600s;
            proxy_read_timeout    600s;
            send_timeout          600s;
            proxy_request_buffering off;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host              $http_host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Real-Port       $remote_port;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header X-Forwarded-Host  $http_host;
            proxy_set_header X-Original-URI    $request_uri;
        }

        # =========================
        # index.html 不缓存
        # =========================
        location = /index.html {
            expires -1;
            add_header Cache-Control "no-store, no-cache, must-revalidate, max-age=0, s-maxage=0" always;
            add_header Pragma "no-cache" always;
            add_header X-Frame-Options "ALLOWALL" always;
            add_header Access-Control-Allow-Origin "*" always;
        }

        # =========================
        # 静态资源缓存
        # =========================
        location ~* \.(js|css|map|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf)$ {
            expires 7d;
            add_header Cache-Control "public, max-age=604800" always;
            access_log off;
            try_files $uri =404;
        }

        # =========================
        # 多应用入口
        # =========================
        location ~ ^/apps/(([0-9]+)|(gymk))\d+ {
            index index.html index.htm;
            try_files $uri $uri/ /apps/$1/index.html;
            expires -1;
            add_header Cache-Control "no-store, no-cache, must-revalidate, max-age=0, s-maxage=0" always;
            add_header Pragma "no-cache" always;
        }

        # =========================
        # 默认前端 SPA 入口
        # =========================
        location / {
            index index.html index.htm;
            try_files $uri $uri/ /index.html;
            add_header X-Frame-Options "ALLOWALL" always;
            expires -1;
            add_header Cache-Control "no-store, no-cache, must-revalidate, max-age=0, s-maxage=0" always;
            add_header Pragma "no-cache" always;
            add_header Access-Control-Allow-Origin "*" always;
            add_header Access-Control-Allow-Methods "GET, POST, OPTIONS" always;
            add_header Access-Control-Allow-Headers "DNT,X-Mx-ReqToken,Keep-Alive,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Authorization" always;
            if ($request_method = OPTIONS) {
                return 204;
            }
        }

        error_page 500 502 503 504 /50x.html;
        location = /50x.html {
            root /usr/share/nginx/html;
        }
    }
`,
		},
		{
			Name:        "ConfigMap (web-cdr)",
			Code:        "configmap-web-cdr",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "web-cdr Nginx ConfigMap",
			Content: `apiVersion: v1
kind: ConfigMap
metadata:
  name: web-cdr
  namespace: ${namespace}
data:
  web-cdr.conf: |
    server {
        listen 80;

        # gzip config
        gzip on;
        gzip_min_length 1k;
        gzip_comp_level 9;
        gzip_types text/plain application/javascript application/x-javascript text/css application/xml text/javascript application/x-httpd-php image/jpeg image/gif image/png;
        gzip_vary on;
        gzip_disable "MSIE [1-6]\.";

        root /usr/share/nginx/html;

        location / {
            try_files $uri $uri/ /index.html;
            error_page 405 =200 http://$host$request_uri;
            proxy_set_header Host      $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            add_header X-Frame-Options ALLOWALL;
            add_header Cache-Control "no-cache";
            add_header Access-Control-Allow-Origin *;
            add_header Access-Control-Allow-Methods 'GET, POST, OPTIONS';
            add_header Access-Control-Allow-Headers 'DNT,X-Mx-ReqToken,Keep-Alive,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Authorization';
        }

        location /api {
            proxy_pass http://his-gateway:8080;
            rewrite ^/api/(.*)$ /$1 break;
            proxy_set_header Host              $http_host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Original-URI    $uri;
            proxy_set_header X-Forwarded-Proto $scheme;
            error_page 405 =200 http://$host$request_uri;
        }
    }
`,
		},
		{
			Name:        "ConfigMap (web-opm)",
			Code:        "configmap-web-opm",
			Category:    "k8s",
			ContentType: "yaml",
			Description: "web-opm Nginx ConfigMap",
			Content: `apiVersion: v1
kind: ConfigMap
metadata:
  name: web-opm
  namespace: ${namespace}
data:
  web-opm.conf: |
    server {
        listen 80;

        # gzip config
        gzip on;
        gzip_min_length 1k;
        gzip_comp_level 9;
        gzip_types text/plain application/javascript application/x-javascript text/css application/xml text/javascript application/x-httpd-php image/jpeg image/gif image/png;
        gzip_vary on;
        gzip_disable "MSIE [1-6]\.";

        root /usr/share/nginx/html;

        location / {
            try_files $uri $uri/ /index.html;
            error_page 405 =200 http://$host$request_uri;
            add_header Cache-Control "no-cache";
            add_header Access-Control-Allow-Origin *;
            add_header Access-Control-Allow-Methods 'GET, POST, OPTIONS';
            add_header Access-Control-Allow-Headers 'DNT,X-Mx-ReqToken,Keep-Alive,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Authorization';
        }

        location /api {
            proxy_pass http://his-gateway:8080;
            rewrite ^/api/(.*)$ /$1 break;
            proxy_set_header X-Original-URI    $uri;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Host              $http_host;
            proxy_set_header X-Real-IP         $remote_addr;
            error_page 405 =200 http://$host$request_uri;
        }

        location /graphql {
            proxy_pass ${skywalkingGraphqlUrl};
            proxy_set_header X-Original-URI    $uri;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Host              $http_host;
            proxy_set_header X-Real-IP         $remote_addr;
        }
    }
`,
		},
		{
			Name:        "Java App",
			Code:        "app-sh-java",
			Category:    "shell",
			ContentType: "shell",
			Description: "Java microservice container startup script",
			Content: `#!/bin/bash
java -javaagent:/opt/skywalking-agent.jar \
  -Dskywalking.agent.service_name=${appName} \
  -Dskywalking.collector.backend_service=${skywalkingOapUrl} \
  -XX:+UseContainerSupport \
  -XX:InitialRAMPercentage=50.0 \
  -XX:MaxRAMPercentage=50.0 \
  -XX:ActiveProcessorCount=1 \
  -XX:+HeapDumpOnOutOfMemoryError \
  -XX:HeapDumpPath=/opt/logs/oom_$(date +%Y%m%d_%H%M%S).hprof \
  -XX:+UseG1GC \
  -XX:+PrintGCDetails \
  -Dspring.profiles.active=dev \
  -Dspring.cloud.nacos.username=${nacosUser} \
  -Dspring.cloud.nacos.password=${nacosPass} \
  -Dlogs.dir=/opt/logs \
  -Dfile.encoding=UTF-8 \
  -jar /opt/app.jar
`,
		},
		{
			Name:        "Java Offline",
			Code:        "delete-app-java",
			Category:    "shell",
			ContentType: "shell",
			Description: "Java microservice graceful shutdown script",
			Content: `#!/bin/bash
curl -X PUT "http://nacos.default.svc:8848/nacos/v1/ns/instance?namespaceId=${NACOS_NAMESPACE_ID}&serviceName=$APP_SERVICENAME&ip=$MY_POD_IP&port=8080&enable=false&username=$NACOS_USER&password=$NACOS_PASS"
sleep 40
PID=$(pidof java) && kill -SIGTERM $PID
`,
		},
	}

	for _, item := range items {
		var existing model.ConfigItem
		if DB.Where("code = ?", item.Code).First(&existing).Error != nil {
			DB.Create(&item)
		}
	}

	logger.Log.Info("Personal config items seeding completed")
}
