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
  password: '123456',
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
    <!-- Left: Visual (70%) -->
    <div class="login-visual">
      <div class="shape shape-1"></div>
      <div class="shape shape-2"></div>
      <div class="shape shape-3"></div>

      <div class="visual-content">
        <h2 class="visual-heading">构建 · 发布 · 交付</h2>
        <p class="visual-sub">从代码到制品，一站式自动化流水线</p>

        <!-- SVG Illustration -->
        <div class="illustration-wrapper">
          <img src="/login-illustration.svg" alt="illustration" class="illustration-svg" />
        </div>

        <!-- Stats -->
        <div class="visual-stats">
          <div class="stat-item">
            <div class="stat-num">100+</div>
            <div class="stat-label">注册应用</div>
          </div>
          <div class="stat-item">
            <div class="stat-num">5000+</div>
            <div class="stat-label">累计构建</div>
          </div>
          <div class="stat-item">
            <div class="stat-num">99.2%</div>
            <div class="stat-label">成功率</div>
          </div>
        </div>
      </div>

      <div class="visual-edge"></div>
    </div>

    <!-- Right: Login Form (30%) -->
    <div class="login-form-side">
      <!-- Brand at top -->
      <div class="brand-top">
        <img src="/brand-logo.svg" alt="CloudHIS智能管理平台" class="brand-logo" />
      </div>

      <!-- Form centered -->
      <div class="form-area">
        <h2 class="welcome-text">欢迎回来</h2>
        <p class="brand-desc">请登录您的管理员账号</p>

        <el-form :model="form" class="login-form" @submit.prevent="handleLogin" label-position="top">
          <el-form-item label="用户名" required>
            <el-input
              v-model="form.username"
              placeholder="请输入用户名"
              size="large"
              prefix-icon="User"
            />
          </el-form-item>
          <el-form-item label="密码" required>
            <el-input
              v-model="form.password"
              type="password"
              placeholder="请输入密码"
              size="large"
              prefix-icon="Lock"
              show-password
              @keyup.enter="handleLogin"
            />
          </el-form-item>
          <el-button
            type="primary"
            size="large"
            class="login-btn"
            :loading="loading"
            @click="handleLogin"
          >
            登 录
          </el-button>
        </el-form>

        <div class="form-footer">
          CloudHIS v1.0 · 智能管理平台
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  height: 100vh;
  display: flex;
  overflow: hidden;
}

/* ===== Left Visual (70%) ===== */
.login-visual {
  width: 70%;
  background: linear-gradient(160deg, #4facfe 0%, #00f2fe 100%);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 40px 60px;
  position: relative;
  overflow: hidden;
}

.shape {
  position: absolute;
  border-radius: 50%;
  opacity: 0.1;
  background: #fff;
}

.shape-1 {
  width: 450px;
  height: 450px;
  top: -120px;
  left: -80px;
  animation: float 8s ease-in-out infinite;
}

.shape-2 {
  width: 300px;
  height: 300px;
  bottom: -80px;
  right: 20%;
  animation: float 6s ease-in-out infinite reverse;
}

.shape-3 {
  width: 150px;
  height: 150px;
  top: 60%;
  left: 50%;
  opacity: 0.06;
  animation: float 10s ease-in-out infinite;
}

@keyframes float {
  0%, 100% { transform: translateY(0); }
  50% { transform: translateY(-20px); }
}

.visual-content {
  position: relative;
  z-index: 1;
  text-align: center;
  max-width: 560px;
}

.visual-heading {
  font-size: 32px;
  font-weight: 700;
  color: #fff;
  margin: 0 0 8px 0;
  letter-spacing: 3px;
}

.visual-sub {
  font-size: 15px;
  color: rgba(255, 255, 255, 0.85);
  margin: 0 0 24px 0;
}

/* Illustration */
.illustration-wrapper {
  margin-bottom: 28px;
}

.illustration-svg {
  width: 100%;
  max-width: 420px;
  height: auto;
}

/* Stats */
.visual-stats {
  display: flex;
  justify-content: center;
  gap: 56px;
}

.stat-item { text-align: center; }

.stat-num {
  font-size: 24px;
  font-weight: 700;
  color: #fff;
}

.stat-label {
  font-size: 12px;
  color: rgba(255, 255, 255, 0.7);
  margin-top: 4px;
}

/* Irregular edge */
.visual-edge {
  position: absolute;
  top: 0;
  right: -1px;
  width: 80px;
  height: 100%;
  z-index: 2;
  clip-path: polygon(
    60% 0%,
    100% 0%,
    100% 100%,
    60% 100%,
    30% 92%,
    55% 80%,
    25% 68%,
    50% 55%,
    20% 42%,
    48% 30%,
    25% 18%,
    55% 6%
  );
}

/* ===== Right Form (30%) ===== */
.login-form-side {
  width: 30%;
  min-width: 340px;
  background: linear-gradient(180deg, #f8fbff 0%, #eef5ff 50%, #e8f4fd 100%);
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 60px 40px 40px;
}

.brand-top {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
}

.visual-edge {
  background: linear-gradient(180deg, #f8fbff 0%, #eef5ff 50%, #e8f4fd 100%);
}

.form-area {
  width: 100%;
  max-width: 300px;
  margin-top: auto;
  margin-bottom: auto;
}

.brand-logo {
  width: 220px;
  height: 66px;
  display: block;
}

.brand-desc {
  font-size: 13px;
  color: #909399;
  margin: 0 0 32px 0;
}

.welcome-text {
  font-size: 24px;
  font-weight: 700;
  color: #303133;
  margin: 0 0 6px 0;
}

.login-form :deep(.el-input__wrapper) {
  padding: 8px 12px;
  border-radius: 6px;
  background: #fff;
}

.login-btn {
  width: 100%;
  height: 42px;
  font-size: 14px;
  font-weight: 600;
  border-radius: 6px;
  margin-top: 8px;
  background: linear-gradient(135deg, #409eff 0%, #2b7de9 100%);
  border: none;
}

.login-btn:hover {
  background: linear-gradient(135deg, #66b1ff 0%, #409eff 100%);
}

.form-footer {
  text-align: center;
  margin-top: 36px;
  font-size: 11px;
  color: #b0b8c4;
}

/* Dark mode */
html.dark .login-form-side {
  background: linear-gradient(180deg, #1a1a1a 0%, #1e2430 50%, #1a2030 100%);
}

html.dark .visual-edge {
  background: linear-gradient(180deg, #1a1a1a 0%, #1e2430 50%, #1a2030 100%);
}

html.dark .welcome-text {
  color: #ffffffd9;
}

html.dark .brand-desc {
  color: #ffffff80;
}

html.dark .form-footer {
  color: #ffffff4d;
}

html.dark .login-form :deep(.el-input__wrapper) {
  background: #242424;
}
</style>
