import request, { type PageResult } from './request'

export interface NotificationMsg {
  id: number
  type: string // build_complete / deploy_complete / deploy_failed / announcement
  title: string
  content: string
  level: string // info / success / warning / error
  read: boolean
  pipelineId: number
  createdAt: string
}

export const listNotificationMsgs = (page = 1, pageSize = 20) =>
  request.get<any, PageResult<NotificationMsg>>('/notification-msgs', { params: { page, pageSize } })

export const getUnreadCount = () =>
  request.get<any, { count: number }>('/notification-msgs/unread-count')

export const markRead = (id: number) =>
  request.put<any, null>(`/notification-msgs/${id}/read`)

export const markAllRead = () =>
  request.put<any, null>('/notification-msgs/read-all')

export const publishAnnouncement = (title: string, content: string) =>
  request.post<any, null>('/notification-msgs/announce', { title, content })
