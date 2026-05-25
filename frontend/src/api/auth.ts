import request from './request'

export interface LoginParams {
  username: string
  password: string
}

export interface User {
  id: number
  username: string
  email: string
  phone: string
  department: string
}

export interface LoginResult {
  token: string
  expiresAt: string
  user: User
}

export function login(params: LoginParams) {
  return request.post<any, LoginResult>('/auth/login', params)
}

export function logout() {
  return request.post<any, null>('/auth/logout')
}

export function getProfile() {
  return request.get<any, User>('/auth/profile')
}

export function updateProfile(data: Partial<User>) {
  return request.put<any, User>('/auth/profile', data)
}

export function sendVerifyCode(email: string) {
  return request.post<any, null>('/auth/send-code', { email })
}

export function changePassword(data: { email: string; code: string; newPassword: string; confirmPassword: string }) {
  return request.post<any, null>('/auth/change-password', data)
}
