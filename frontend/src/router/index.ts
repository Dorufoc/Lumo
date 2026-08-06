import { createRouter, createWebHistory } from 'vue-router'

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
    { path: '/plan', name: 'plan', component: () => import('@/views/PlanView.vue'), meta: { title: '计划' } },
    { path: '/library', name: 'library', component: () => import('@/views/LibraryView.vue'), meta: { title: '题库与资料' } },
    { path: '/tutor', name: 'tutor', component: () => import('@/views/TutorView.vue'), meta: { title: 'AI Tutor' } },
    { path: '/settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { title: '设置与数据' } },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

export default router
