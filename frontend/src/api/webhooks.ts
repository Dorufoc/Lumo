// Webhook 出站分发 API（完整设计文档 4.23 / Todo 31）。
// 订阅在工作区内；secret_ref 指向 secrets.json 中已有的键名（设置中配置）。
import { call } from '@/api/client'
import type {
  DeleteResult,
  WebhookDeleteReq,
  WebhookListReq,
  WebhookSubscribeReq,
  WebhookSubscription,
  WebhookTestSendReq,
  WebhookTestSendResp,
} from '@/api/types'

/** 创建 Webhook 订阅（url / event_types[] / secret_ref）。 */
export function webhookSubscribe(req: WebhookSubscribeReq): Promise<WebhookSubscription> {
  return call<WebhookSubscription>('WebhookSubscribe', { ...req })
}

/** 测试发送：对订阅 URL 强制发送一次测试事件（不落库、不进重试队列）。 */
export function webhookTestSend(req: WebhookTestSendReq): Promise<WebhookTestSendResp> {
  return call<WebhookTestSendResp>('WebhookTestSend', { ...req })
}

/** 删除订阅及其投递记录（存在进行中投递时返回 CONFLICT）。 */
export function webhookDelete(req: WebhookDeleteReq): Promise<DeleteResult> {
  return call<DeleteResult>('WebhookDelete', { ...req })
}

/** 列出工作区内订阅（新→旧）。 */
export function webhookList(req: WebhookListReq): Promise<WebhookSubscription[]> {
  return call<WebhookSubscription[]>('WebhookList', { ...req })
}
