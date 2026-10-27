<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useUserStore } from '../stores/user'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()
const loading = ref(false)

const form = ref({
  username: 'admin',
  password: 'cloudhis@2123',
})

async function handleLogin() {
  if (!form.value.username.trim()) {
    ElMessage.warning('请输入用户名')
    return
  }
  if (!form.value.password.trim()) {
    ElMessage.warning('请输入密码')
    return
  }
  loading.value = true
  try {
    await userStore.login(form.value.username, form.value.password)
    ElMessage.success('登录成功')
    const redirect = (route.query.redirect as string) || '/dashboard'
    router.push(redirect)
  } catch (e: any) {
    // Error message handled by axios interceptor
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <section class="login-visual" aria-label="CloudHIS 平台介绍">
      <div class="visual-top"><img src="/logo.svg" alt="" /><span>CloudHIS </span></div>
      <div class="visual-content"><h1>构建与运维</h1>
        <div class="capability-list"><div><span class="capability-icon"><Box /></span><span><strong>制品与更新</strong></span><i>01</i></div><div><span class="capability-icon"><Cloudy /></span><span><strong>集群与主机</strong></span><i>02</i></div><div><span class="capability-icon"><Files /></span><span><strong>数据与日志</strong></span><i>03</i></div></div>
      </div>
    </section>
    <section class="login-form-side">
      <div class="form-area"><h2 class="welcome-text">登录工作空间</h2>
        <el-form :model="form" class="login-form" @submit.prevent="handleLogin" label-position="top">
          <el-form-item label="用户名" required><el-input v-model="form.username" placeholder="请输入用户名" size="large" prefix-icon="User" autocomplete="username" /></el-form-item>
          <el-form-item label="密码" required><el-input v-model="form.password" type="password" placeholder="请输入密码" size="large" prefix-icon="Lock" show-password autocomplete="current-password" @keyup.enter="handleLogin" /></el-form-item>
          <el-button type="primary" size="large" class="login-btn" :loading="loading" @click="handleLogin">登录 <span>→</span></el-button>
        </el-form>
      </div>
    </section>
  </div>
</template>

<style scoped>
.login-page { min-height: 100vh; min-height: 100dvh; display: flex; background: var(--ui-panel); }
.login-visual { flex: 1.05; min-width: 0; background: #102441; color: #fff; position: relative; display: flex; flex-direction: column; padding: 38px 56px; overflow: hidden; }
.login-visual::before { content: ''; position: absolute; inset: 0; background-image: linear-gradient(rgb(151 196 235 / 4%) 1px, transparent 1px), linear-gradient(90deg, rgb(151 196 235 / 4%) 1px, transparent 1px), radial-gradient(ellipse at 85% 15%, #174f65 0%, transparent 58%); background-size: 48px 48px, 48px 48px, auto; pointer-events: none; }
.visual-top { display: flex; gap: 11px; align-items: center; position: relative; }.visual-top img { width: 35px; height: 35px; }.visual-top > span { font-size: 21px; font-weight: 650; letter-spacing: -.6px; }
.visual-content { width: 100%; max-width: 440px; margin: auto; padding: 65px 0; position: relative; }.visual-content h1 { font-size: clamp(35px, 3.2vw, 52px); font-weight: 550; letter-spacing: 2px; line-height: 1.45; margin: 21px 0; }
.capability-list { margin-top: 43px; border-top: 1px solid #29435e; }.capability-list > div { display: flex; align-items: center; gap: 16px; padding: 21px 0; border-bottom: 1px solid #29435e; }.capability-icon { width: 35px; height: 35px; padding: 9px; border: 1px solid #365573; border-radius: 9px; color: #7dbedf; background: rgb(117 178 218 / 5%); }.capability-list strong { display: block; font-size: 13px; font-weight: 500; color: #d7e5f3; }.capability-list i { margin-left: auto; font-style: normal; font-family: var(--ui-font-mono); font-size: 10px; color: #6285a8; }
.login-form-side { flex: 1; min-width: 390px; background: var(--ui-panel); padding: 43px 60px 30px; display: flex; flex-direction: column; }.form-area { width: 100%; max-width: 345px; margin: auto; padding: 65px 0; }.welcome-text { margin-bottom: 30px; font-size: 28px; font-weight: 600; color: var(--ui-heading); letter-spacing: -.6px; margin-top: 5px; }
.login-form :deep(.el-form-item__label) { font-size: 12px; margin-bottom: 8px; }.login-form :deep(.el-input__wrapper) { background: var(--ui-panel-soft); padding: 6px 13px; border-radius: 8px; }.login-form :deep(.el-input__prefix) { margin-right: 4px; }.login-btn { width: 100%; height: 45px; font-size: 13px; margin-top: 8px; }.login-btn span { margin-left: 16px; }
@media (max-width: 1050px) { .login-visual { padding: 32px; }.login-form-side { padding-inline: 36px; } }
@media (max-width: 760px) { .login-visual { display: none; }.login-form-side { min-width: 0; padding: 32px; }.form-area { padding-block: 70px; } }
</style>
