import { connectorImage, probeServiceName } from './common.js';

export function webhook(c) {
  return [
    'receivers:',
    '  - name: kubepilot',
    '    webhook_configs:',
    '      - url: ' + JSON.stringify(c.publicUrl + '/ingest/alertmanager'),
    '        send_resolved: true',
    '        http_config:',
    '          authorization:',
    '            type: Bearer',
    '            credentials: ' + JSON.stringify(c.webhookToken),
  ].join('\n');
}
export function manifest(c) {
  connectorImage(c.scope.image);
  const probe = (path) => ({
    exec: {
      command: [
        'node',
        '-e',
        "fetch('http://127.0.0.1:' + process.env.CONNECTOR_HEALTH_PORT + '/" +
          path +
          "').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))",
      ],
    },
    periodSeconds: 10,
    timeoutSeconds: 3,
    failureThreshold: 3,
  });
  const s = c.scope,
    n = 'kubepilot-connector',
    ns = s.namespace,
    metadata = { name: n, namespace: ns };
  const obj = (apiVersion, kind, spec) => ({ apiVersion, kind, metadata, ...spec });
  const rules = [
    { apiGroups: [''], resources: ['pods', 'events'], verbs: ['list'] },
    { apiGroups: [''], resources: ['pods/log'], verbs: ['get'] },
    { apiGroups: ['apps'], resources: ['replicasets'], verbs: ['list'] },
    { apiGroups: ['discovery.k8s.io'], resources: ['endpointslices'], verbs: ['list'] },
  ];
  for (const [kind, group, resource] of [
    ['Deployment', 'apps', 'deployments'],
    ['Service', '', 'services'],
  ]) {
    const names = s.targets.filter((t) => t.kind === kind).map((t) => t.name);
    if (names.length)
      rules.push({
        apiGroups: [group],
        resources: [resource],
        resourceNames: names,
        verbs: [
          'get',
          ...(s.actions.some((a) =>
            kind === 'Service' ? a === 'restore_selector' : a !== 'restore_selector',
          )
            ? ['patch']
            : []),
        ],
      });
  }
  const probeServices = s.targets.map((t) => probeServiceName(t.probeUrl, ns)).filter(Boolean);
  if (probeServices.length)
    rules.push({
      apiGroups: [''],
      resources: ['services'],
      resourceNames: [...new Set(probeServices)],
      verbs: ['get'],
    });
  const labels = { app: n },
    cfg = {
      protocol: 3,
      serverUrl: c.publicUrl,
      allowHttp: c.allowHttp,
      token: c.connectorToken,
      generation: c.generation,
      scope: s,
    };
  return JSON.stringify(
    {
      apiVersion: 'v1',
      kind: 'List',
      items: [
        obj('v1', 'ServiceAccount', {}),
        obj('rbac.authorization.k8s.io/v1', 'Role', { rules }),
        obj('rbac.authorization.k8s.io/v1', 'RoleBinding', {
          roleRef: { apiGroup: 'rbac.authorization.k8s.io', kind: 'Role', name: n },
          subjects: [{ kind: 'ServiceAccount', ...metadata }],
        }),
        obj('v1', 'Secret', { type: 'Opaque', stringData: { 'config.json': JSON.stringify(cfg) } }),
        obj('v1', 'PersistentVolumeClaim', {
          spec: { accessModes: ['ReadWriteOnce'], resources: { requests: { storage: '64Mi' } } },
        }),
        obj('apps/v1', 'Deployment', {
          spec: {
            replicas: 1,
            strategy: { type: 'Recreate' },
            selector: { matchLabels: labels },
            template: {
              metadata: { labels, annotations: { 'kubepilot/generation': String(c.generation) } },
              spec: {
                serviceAccountName: n,
                terminationGracePeriodSeconds: 45,
                securityContext: {
                  runAsNonRoot: true,
                  runAsUser: 1000,
                  fsGroup: 1000,
                  seccompProfile: { type: 'RuntimeDefault' },
                },
                containers: [
                  {
                    name: n,
                    image: s.image,
                    imagePullPolicy: 'IfNotPresent',
                    command: ['node', 'connector/main.js'],
                    readinessProbe: probe('ready'),
                    livenessProbe: probe('live'),
                    env: [
                      { name: 'CONNECTOR_CONFIG', value: '/config/config.json' },
                      { name: 'CONNECTOR_DATA', value: '/data' },
                      { name: 'CONNECTOR_HEALTH_PORT', value: '18086' },
                    ],
                    securityContext: {
                      readOnlyRootFilesystem: true,
                      allowPrivilegeEscalation: false,
                      capabilities: { drop: ['ALL'] },
                    },
                    resources: {
                      requests: { cpu: '20m', memory: '48Mi' },
                      limits: { cpu: '500m', memory: '192Mi' },
                    },
                    volumeMounts: [
                      { name: 'config', mountPath: '/config', readOnly: true },
                      { name: 'data', mountPath: '/data' },
                    ],
                  },
                ],
                volumes: [
                  { name: 'config', secret: { secretName: n, defaultMode: 288 } },
                  { name: 'data', persistentVolumeClaim: { claimName: n } },
                ],
              },
            },
          },
        }),
      ],
    },
    null,
    2,
  );
}
