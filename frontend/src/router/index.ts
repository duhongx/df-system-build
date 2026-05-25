import { createRouter, createWebHistory } from 'vue-router'
import DefaultLayout from '../layouts/DefaultLayout.vue'
import { getToken } from '../api/request'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('../views/LoginView.vue'),
    meta: { title: '登录', public: true }
  },
  {
    path: '/',
    component: DefaultLayout,
    redirect: '/dashboard',
    children: [
      { path: 'dashboard', name: 'Dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '工作台' } },
      { path: 'tasks', name: 'TaskList', component: () => import('../views/TaskListView.vue'), meta: { title: '任务列表' } },
      { path: 'batch-deploy', name: 'BatchDeploy', component: () => import('../views/BatchDeployView.vue'), meta: { title: '批量部署' } },
      { path: 'build-queue', name: 'BuildQueue', component: () => import('../views/BuildQueueView.vue'), meta: { title: '构建队列' } },
      { path: 'release', name: 'ReleaseList', component: () => import('../views/ReleaseListView.vue'), meta: { title: '构建历史' } },
      { path: 'release/:id', name: 'PipelineDetail', component: () => import('../views/PipelineDetailView.vue'), meta: { title: 'Pipeline 详情' } },
      { path: 'artifacts', name: 'ArtifactList', component: () => import('../views/ArtifactListView.vue'), meta: { title: '制品记录' } },
      { path: 'servers', name: 'ServerMgmt', component: () => import('../views/ServerMgmtView.vue'), meta: { title: '服务器管理' } },
      { path: 'servers/:id/terminal', name: 'ServerTerminal', component: () => import('../views/ServerTerminalView.vue'), meta: { title: 'WebSSH 终端' } },
      { path: 'servers/:id/files', name: 'ServerFiles', component: () => import('../views/ServerFilesView.vue'), meta: { title: '文件管理' } },
      { path: 'servers/:id/monitor', name: 'ServerMonitor', component: () => import('../views/ServerMonitorView.vue'), meta: { title: '系统监控' } },
      { path: 'kubernetes', redirect: '/kubernetes/overview' },
      { path: 'kubernetes/overview', name: 'K8sOverview', component: () => import('../views/KubernetesView.vue'), meta: { title: '集群概览', k8sTab: 'overview' } },
      { path: 'kubernetes/nodes', name: 'K8sNodes', component: () => import('../views/KubernetesView.vue'), meta: { title: 'Nodes', k8sTab: 'nodes' } },
      { path: 'kubernetes/deployments', name: 'K8sDeployments', component: () => import('../views/KubernetesView.vue'), meta: { title: 'Deployments', k8sTab: 'deployments' } },
      { path: 'kubernetes/pods', name: 'K8sPods', component: () => import('../views/KubernetesView.vue'), meta: { title: 'Pods', k8sTab: 'pods' } },
      { path: 'kubernetes/services', name: 'K8sServices', component: () => import('../views/KubernetesView.vue'), meta: { title: 'Services', k8sTab: 'services' } },
      { path: 'kubernetes/configmaps', name: 'K8sConfigMaps', component: () => import('../views/KubernetesView.vue'), meta: { title: 'ConfigMaps', k8sTab: 'configmaps' } },
      { path: 'kubernetes/ingresses', name: 'K8sIngresses', component: () => import('../views/KubernetesView.vue'), meta: { title: 'Ingress', k8sTab: 'ingresses' } },
      { path: 'settings/general', name: 'SettingsGeneral', component: () => import('../views/SettingsView.vue'), meta: { title: '全局参数', settingsTab: 'general' } },
      { path: 'settings/apps', name: 'SettingsApps', component: () => import('../views/AppListView.vue'), meta: { title: '应用管理', settingsTab: 'apps' } },
      { path: 'settings/registry', redirect: '/settings/environment' },
      { path: 'settings/k8s', redirect: '/settings/environment' },
      { path: 'settings/environment', name: 'SettingsEnvironment', component: () => import('../views/SettingsEnvironmentView.vue'), meta: { title: '环境配置', settingsTab: 'environment' } },
      { path: 'settings/templates', redirect: '/settings/build-configs' },
      { path: 'settings/executors', redirect: '/settings/build-configs' },
      { path: 'settings/build-configs', name: 'SettingsBuildConfigs', component: () => import('../views/BuildConfigView.vue'), meta: { title: '编译配置', settingsTab: 'build-configs' } },
      { path: 'settings/notifications', name: 'SettingsNotifications', component: () => import('../views/SettingsView.vue'), meta: { title: '通知配置', settingsTab: 'notifications' } },
      { path: 'settings/config-items', name: 'SettingsConfigItems', component: () => import('../views/ConfigItemsView.vue'), meta: { title: '配置项管理', settingsTab: 'config-items' } },
      { path: 'settings', redirect: '/settings/general' },
      // Legacy redirects
      { path: 'apps', redirect: '/settings/apps' },
      // Infrastructure
      { path: 'infra/check', name: 'InfraCheck', component: () => import('../views/InfraCheckView.vue'), meta: { title: '部署检测' } },
      { path: 'infra/plan', name: 'InfraPlan', component: () => import('../views/InfraPlanView.vue'), meta: { title: '部署规划' } },
      { path: 'infra/execute', name: 'InfraExecute', component: () => import('../views/InfraExecuteView.vue'), meta: { title: '执行部署' } },
      { path: 'infra/environment', name: 'InfraEnvironment', component: () => import('../views/InfraEnvironmentView.vue'), meta: { title: '环境清单' } },
    ]
  }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

router.beforeEach((to, _from, next) => {
  const token = getToken()
  if (to.meta.public) {
    next()
    return
  }
  if (!token) {
    next({ path: '/login', query: { redirect: to.fullPath } })
    return
  }
  next()
})

export default router
