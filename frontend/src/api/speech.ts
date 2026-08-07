// 口语练习与语音合成 API（API 设计文档 7.16 / 完整设计文档 4.18）。
// 后端绑定方法：TTSPlay / SpeakingSubmit / SpeakingResultGet / SpeakingUpload(multipart)。
import { call, upload } from '@/api/client'
import type {
  SpeakingResult,
  SpeakingResultGetReq,
  SpeakingSubmitReq,
  TTSPlayReq,
  TTSResult,
  UploadedFile,
} from '@/api/types'

/** 合成引用文本的语音（question/note/flashcard/document）。 */
export function ttsPlay(req: TTSPlayReq): Promise<TTSResult> {
  return call<TTSResult>('TTSPlay', { ...req })
}

/** 上传口语录音（wav/mp3/m4a，≤20MB），返回 uploads 相对路径。 */
export function speakingUpload(file: File, userId: string): Promise<UploadedFile> {
  return upload<UploadedFile>('SpeakingUpload', file, { user_id: userId })
}

/** 提交口语测评：ASR 转写 + 分维度评分，完成后发布 grading:updated。 */
export function speakingSubmit(req: SpeakingSubmitReq): Promise<SpeakingResult> {
  return call<SpeakingResult>('SpeakingSubmit', { ...req })
}

/** 按提交获取口语测评结果。 */
export function speakingResultGet(req: SpeakingResultGetReq): Promise<SpeakingResult> {
  return call<SpeakingResult>('SpeakingResultGet', { ...req })
}
