import { createRouter, createWebHistory } from 'vue-router'
import { useSessionStore } from '@/stores/session'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'onboarding', component: () => import('@/views/OnboardingView.vue') },
    { path: '/dashboard', name: 'dashboard', component: () => import('@/views/DashboardView.vue'), meta: { title: '今日' } },
    { path: '/practice/:sessionId?', name: 'practice', component: () => import('@/views/PracticeView.vue'), meta: { title: '练习' } },
    { path: '/result/:sessionId', name: 'result', component: () => import('@/views/PracticeResultView.vue'), meta: { title: '练习结果' } },
    { path: '/review', name: 'review', component: () => import('@/views/ReviewView.vue'), meta: { title: '错题复习' } },
    { path: '/flashcards', name: 'flashcards', component: () => import('@/views/FlashcardsView.vue'), meta: { title: '闪卡' } },
    { path: '/exams', name: 'exams', component: () => import('@/views/ExamView.vue'), meta: { title: '组卷考试' } },
    { path: '/notes', name: 'notes', component: () => import('@/views/NotesView.vue'), meta: { title: '笔记' } },
    { path: '/checkin', name: 'checkin', component: () => import('@/views/CheckinView.vue'), meta: { title: '打卡成就' } },
    { path: '/focus', name: 'focus', component: () => import('@/views/FocusView.vue'), meta: { title: '专注' } },
    { path: '/notifications', name: 'notifications', component: () => import('@/views/NotificationsView.vue'), meta: { title: '通知' } },
    { path: '/health', name: 'health', component: () => import('@/views/HealthView.vue'), meta: { title: '健康' } },
    { path: '/reports', name: 'reports', component: () => import('@/views/ReportsView.vue'), meta: { title: '报告' } },
    { path: '/plan', name: 'plan', component: () => import('@/views/PlanView.vue'), meta: { title: '计划' } },
    { path: '/calendar', name: 'calendar', component: () => import('@/views/CalendarView.vue'), meta: { title: '日历' } },
    { path: '/library', name: 'library', component: () => import('@/views/LibraryView.vue'), meta: { title: '题库与资料' } },
    { path: '/tutor', name: 'tutor', component: () => import('@/views/TutorView.vue'), meta: { title: 'AI Tutor' } },
    { path: '/classes', name: 'classes', component: () => import('@/views/ClassesView.vue'), meta: { title: '班级', roles: ['teacher', 'student'] } },
    { path: '/assignments', name: 'assignments', component: () => import('@/views/AssignmentsView.vue'), meta: { title: '作业', roles: ['teacher', 'student'] } },
    { path: '/family', name: 'family', component: () => import('@/views/FamilyView.vue'), meta: { title: '家庭', roles: ['parent'] } },
    { path: '/knowledge-graph', name: 'knowledgeGraph', component: () => import('@/views/KnowledgeGraph.vue'), meta: { title: '知识图谱' } },
    { path: '/plugins', name: 'plugins', component: () => import('@/views/plugins/Plugins.vue'), meta: { title: '插件' } },
    { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { title: '设置与数据' } },
    { path: '/admin', name: 'admin', component: () => import('@/views/AdminView.vue'), meta: { title: '管理端' } },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

// 路由级角色守卫：路由声明 meta.roles 时校验当前用户角色；
// 会话未就绪（bootstrap 前）放行，由 App.vue 统一处理引导态。
router.beforeEach((to) => {
  const allowed = to.meta.roles as string[] | undefined
  if (!allowed || allowed.length === 0) return true
  const session = useSessionStore()
  if (!session.user) return true
  if (allowed.includes(session.user.role)) return true
  return { name: 'dashboard' }
})

export default router
