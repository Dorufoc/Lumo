// 作业 API（API 文档 7.11 / 完整设计文档 4.22）。
import { call } from '@/api/client'
import type {
  Appeal,
  AppealCreateReq,
  AppealListReq,
  AppealResolveReq,
  Assignment,
  AssignmentCreateReq,
  AssignmentGradeReq,
  AssignmentListReq,
  AssignmentPublishReq,
  AssignmentSubmission,
  AssignmentSubmissionListReq,
  AssignmentSubmitReq,
} from '@/api/types'

/** 创建作业（教师，草稿态）。 */
export function assignmentCreate(req: AssignmentCreateReq): Promise<Assignment> {
  return call<Assignment>('AssignmentCreate', { ...req })
}

/** 发布作业（教师，乐观锁）。 */
export function assignmentPublish(req: AssignmentPublishReq): Promise<Assignment> {
  return call<Assignment>('AssignmentPublish', { ...req })
}

/** 提交作业（班级 active 学生）。 */
export function assignmentSubmit(req: AssignmentSubmitReq): Promise<AssignmentSubmission> {
  return call<AssignmentSubmission>('AssignmentSubmit', { ...req })
}

/** 作业列表（教师=创建班级；学生=加入班级）。 */
export function assignmentList(req: AssignmentListReq): Promise<Assignment[]> {
  return call<Assignment[]>('AssignmentList', { ...req })
}

/** 作业提交名单（教师）。 */
export function assignmentSubmissionList(req: AssignmentSubmissionListReq): Promise<AssignmentSubmission[]> {
  return call<AssignmentSubmission[]>('AssignmentSubmissionList', { ...req })
}

/** 批阅作业（教师，乐观锁；pre_grade=true 时仅 EssayGrader 预批预览）。 */
export function assignmentGrade(req: AssignmentGradeReq): Promise<AssignmentSubmission> {
  return call<AssignmentSubmission>('AssignmentGrade', { ...req })
}

/** 学生提交申诉。 */
export function appealCreate(req: AppealCreateReq): Promise<Appeal> {
  return call<Appeal>('AppealCreate', { ...req })
}

/** 教师处理申诉（复议改分）。 */
export function appealResolve(req: AppealResolveReq): Promise<Appeal> {
  return call<Appeal>('AppealResolve', { ...req })
}

/** 教师复议视图：列出某作业全部申诉。 */
export function appealList(req: AppealListReq): Promise<Appeal[]> {
  return call<Appeal[]>('AppealList', { ...req })
}
