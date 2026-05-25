import { defineStore } from 'pinia'
import { ref } from 'vue'
import * as authApi from '../api/auth'
import { setToken, clearToken, getToken } from '../api/request'

export const useUserStore = defineStore('user', () => {
  const user = ref<authApi.User | null>(null)
  const token = ref<string>(getToken())

  async function login(username: string, password: string) {
    const result = await authApi.login({ username, password })
    token.value = result.token
    user.value = result.user
    setToken(result.token)
    return result
  }

  async function loadProfile() {
    if (!token.value) return null
    const profile = await authApi.getProfile()
    user.value = profile
    return profile
  }

  async function logout() {
    try {
      await authApi.logout()
    } catch (_) {
      // ignore
    }
    token.value = ''
    user.value = null
    clearToken()
  }

  return {
    user,
    token,
    login,
    loadProfile,
    logout,
  }
})
