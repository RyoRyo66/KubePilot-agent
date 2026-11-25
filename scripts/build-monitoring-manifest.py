"""Build the local monitoring manifest from reviewable configs; no credentials."""
import json
from pathlib import Path

root = Path(__file__).resolve().parents[1]
config = root / 'deploy' / 'monitoring'
namespace = 'kubepilot-monitoring'
items = [{'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': namespace}}]

def metadata(name):
    return {'name': name, 'namespace': namespace, 'labels': {'app.kubernetes.io/part-of': 'kubepilot-lab'}}

def configmap(name, files):
    items.append({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': metadata(name),
                  'data': {name: (config / name).read_text(encoding='utf-8') for name in files}})

def pvc(name, size):
    items.append({'apiVersion': 'v1', 'kind': 'PersistentVolumeClaim', 'metadata': metadata(name),
                  'spec': {'accessModes': ['ReadWriteOnce'], 'storageClassName': 'standard', 'resources': {'requests': {'storage': size}}}})

def deploy(name, image, port, args, mounts, volumes, ready, memory, limit, user=65534, command=None):
    container = {'name': name, 'image': image, 'imagePullPolicy': 'IfNotPresent',
                 'ports': [{'name': 'http', 'containerPort': port}], 'args': args,
                 'volumeMounts': mounts, 'resources': {'requests': {'cpu': '10m', 'memory': memory}, 'limits': {'cpu': '500m', 'memory': limit}},
                 'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True, 'capabilities': {'drop': ['ALL']}},
                 'readinessProbe': {'httpGet': {'path': ready, 'port': port}, 'initialDelaySeconds': 2, 'periodSeconds': 3},
                 'livenessProbe': {'httpGet': {'path': ready, 'port': port}, 'initialDelaySeconds': 30, 'periodSeconds': 10}}
    if command:
        container['command'] = command
    items.append({'apiVersion': 'apps/v1', 'kind': 'Deployment', 'metadata': metadata(name), 'spec': {
        'replicas': 1, 'strategy': {'type': 'Recreate'}, 'selector': {'matchLabels': {'app': name}},
        'template': {'metadata': {'labels': {'app': name}}, 'spec': {'automountServiceAccountToken': False,
            'securityContext': {'runAsNonRoot': True, 'runAsUser': user, 'runAsGroup': user, 'fsGroup': user},
            'containers': [container], 'volumes': volumes}}}})
    items.append({'apiVersion': 'v1', 'kind': 'Service', 'metadata': metadata(name), 'spec': {
        'selector': {'app': name}, 'ports': [{'name': 'http', 'port': port, 'targetPort': port}]}})

def mount(name, target, readonly=False):
    return {'name': name, 'mountPath': target, 'readOnly': readonly}

def cm(name):
    return {'name': 'config', 'configMap': {'name': name}}

configmap('prometheus', ['prometheus.yml', 'alerts.yml'])
configmap('alertmanager', ['alertmanager.yml'])
configmap('blackbox-exporter', ['blackbox.yml'])
configmap('platform-gateway', ['gateway.conf'])
pvc('prometheus-data', '2Gi')
pvc('alertmanager-data', '1Gi')
deploy('prometheus', 'prom/prometheus:v3.13.5', 9090,
       ['--config.file=/etc/prometheus/prometheus.yml', '--storage.tsdb.path=/prometheus', '--storage.tsdb.retention.time=24h', '--storage.tsdb.retention.size=256MB', '--web.enable-lifecycle'],
       [mount('config', '/etc/prometheus', True), mount('data', '/prometheus')],
       [cm('prometheus'), {'name': 'data', 'persistentVolumeClaim': {'claimName': 'prometheus-data'}}], '/-/ready', '128Mi', '512Mi')
deploy('alertmanager', 'prom/alertmanager:v0.35.0', 9093,
       ['--config.file=/etc/alertmanager/alertmanager.yml', '--storage.path=/alertmanager', '--cluster.listen-address='],
       [mount('config', '/etc/alertmanager', True), mount('data', '/alertmanager'), mount('token', '/etc/kubepilot-token', True)],
       [cm('alertmanager'), {'name': 'data', 'persistentVolumeClaim': {'claimName': 'alertmanager-data'}},
        {'name': 'token', 'secret': {'secretName': 'kubepilot-alertmanager', 'defaultMode': 288}}], '/-/ready', '32Mi', '128Mi')
deploy('blackbox-exporter', 'prom/blackbox-exporter:v0.29.1', 9115, ['--config.file=/etc/blackbox/blackbox.yml'],
       [mount('config', '/etc/blackbox', True)], [cm('blackbox-exporter')], '/metrics', '16Mi', '64Mi')
deploy('platform-gateway', 'nginx:1.28-alpine', 8080, ['-c', '/etc/lab/gateway.conf', '-g', 'daemon off;'],
       [mount('config', '/etc/lab', True), mount('tmp', '/tmp')], [cm('platform-gateway'), {'name': 'tmp', 'emptyDir': {}}],
       '/healthz', '16Mi', '64Mi', user=101, command=['nginx'])
destination = config / 'resources.json'
destination.write_text(json.dumps({'apiVersion': 'v1', 'kind': 'List', 'items': items}, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
print(f'Generated {destination}: {len(items)} Kubernetes objects, no credentials')
