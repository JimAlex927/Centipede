<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'

type User = { id: number; email: string; display_name: string; status: string }
type Context = { id?: number; text: string; location?: string }
type Entry = { id: number; language: string; text: string; normalized_text: string; lemma?: string; definition: string; notes: string; status: string; source: { book: string; chapter: string; location: string }; contexts: Context[] }

const mode = ref<'login' | 'register'>('login')
const token = ref(localStorage.getItem('centipede_access_token') ?? '')
const user = ref<User | null>(null)
const loading = ref(true)
const busy = ref(false)
const errorMessage = ref('')
const notice = ref('')
const readingText = ref('')
const search = ref('')
const languageFilter = ref('')
const entries = ref<Entry[]>([])
const readingInput = ref<HTMLTextAreaElement | null>(null)
const authForm = reactive({ email: '', password: '', display_name: '' })
const entryForm = reactive({ language: 'en', text: '', lemma: '', definition: '', notes: '', book: '', chapter: '', location: '' })
const isAuthenticated = computed(() => Boolean(user.value && token.value))
const languageOptions = computed(() => [...new Set(entries.value.map((entry) => entry.language))])

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Content-Type', 'application/json')
  if (token.value) headers.set('Authorization', `Bearer ${token.value}`)
  const response = await fetch(path, { ...init, headers, credentials: 'include' })
  if (!response.ok) {
    const payload = await response.json().catch(() => null)
    throw new Error(payload?.error?.message ?? '请求没有完成')
  }
  return response.status === 204 ? (undefined as T) : response.json()
}

async function submitAuth() {
  errorMessage.value = ''; busy.value = true
  try {
    const payload = await request<{ user: User; access_token: string }>(`/api/v1/auth/${mode.value}`, { method: 'POST', body: JSON.stringify(authForm) })
    token.value = payload.access_token; localStorage.setItem('centipede_access_token', token.value); user.value = payload.user
    await loadEntries()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '登录失败' } finally { busy.value = false }
}

async function bootstrap() {
  if (!token.value) { loading.value = false; return }
  try {
    let payload: { user: User }
    try { payload = await request<{ user: User }>('/api/v1/auth/me') }
    catch {
      const refreshed = await request<{ user: User; access_token: string }>('/api/v1/auth/refresh', { method: 'POST' })
      token.value = refreshed.access_token; localStorage.setItem('centipede_access_token', token.value); payload = { user: refreshed.user }
    }
    user.value = payload.user; await loadEntries()
  } catch { signOut(false) } finally { loading.value = false }
}

async function loadEntries() {
  const params = new URLSearchParams()
  if (languageFilter.value) params.set('language', languageFilter.value)
  if (search.value.trim()) params.set('q', search.value.trim())
  const payload = await request<{ items: Entry[] }>(`/api/v1/vocabulary/entries?${params}`)
  entries.value = payload.items
}

function captureSelection() {
  const input = readingInput.value
  const selected = input && input.selectionStart !== input.selectionEnd
    ? input.value.slice(input.selectionStart, input.selectionEnd).trim()
    : window.getSelection()?.toString().trim() ?? ''
  if (selected) entryForm.text = selected
}

async function saveEntry() {
  if (!entryForm.text.trim()) { errorMessage.value = '先填入一个词或短语。'; return }
  busy.value = true; errorMessage.value = ''
  try {
    await request('/api/v1/vocabulary/entries', { method: 'POST', body: JSON.stringify({
      language: entryForm.language, text: entryForm.text, lemma: entryForm.lemma, definition: entryForm.definition,
      notes: entryForm.notes, source: { book: entryForm.book, chapter: entryForm.chapter, location: entryForm.location },
      contexts: readingText.value.trim() ? [{ text: readingText.value, location: entryForm.location }] : [],
    }) })
    notice.value = '已收入词汇本。'; entryForm.text = ''; entryForm.lemma = ''; entryForm.definition = ''; entryForm.notes = ''; await loadEntries()
  } catch (error) { errorMessage.value = error instanceof Error ? error.message : '保存失败' } finally { busy.value = false }
}

async function updateStatus(entry: Entry, status: string) {
  try { const updated = await request<Entry>(`/api/v1/vocabulary/entries/${entry.id}/status`, { method: 'PATCH', body: JSON.stringify({ status }) }); Object.assign(entry, updated) }
  catch (error) { errorMessage.value = error instanceof Error ? error.message : '状态更新失败' }
}

async function signOut(callServer = true) {
  if (callServer) await request('/api/v1/auth/logout', { method: 'POST' }).catch(() => undefined)
  token.value = ''; user.value = null; entries.value = []; localStorage.removeItem('centipede_access_token')
}

function statusLabel(status: string) { return { new: '新收', learning: '学习中', known: '已掌握', paused: '搁置' }[status] ?? status }
onMounted(bootstrap)
</script>

<template>
  <main class="shell">
    <div v-if="loading" class="loading">正在打开你的阅读桌……</div>
    <section v-else-if="!isAuthenticated" class="auth-page">
      <div class="auth-copy">
        <p class="eyebrow">CENTIPEDE / READING DESK</p>
        <h1>把读到的词，<em>留下来。</em></h1>
        <p class="lede">一个给外文阅读者的轻巧词汇本。它记住词，也记住它出现时的那一页、那句话和你的当下。</p>
        <div class="paper-note"><span>✦</span> 英语只是开始。每一种语言都值得被好好读过。</div>
      </div>
      <form class="auth-card" @submit.prevent="submitAuth">
        <div class="tabs"><button type="button" :class="{ active: mode === 'login' }" @click="mode = 'login'">登录</button><button type="button" :class="{ active: mode === 'register' }" @click="mode = 'register'">注册</button></div>
        <h2>{{ mode === 'login' ? '欢迎回来' : '开一张新书签' }}</h2><p class="muted">{{ mode === 'login' ? '继续上次停下的地方。' : '从下一页遇见的第一个陌生词开始。' }}</p>
        <label>邮箱<input v-model="authForm.email" type="email" autocomplete="email" required /></label>
        <label v-if="mode === 'register'">称呼<input v-model="authForm.display_name" autocomplete="name" /></label>
        <label>密码<input v-model="authForm.password" type="password" minlength="8" autocomplete="current-password" required /></label>
        <p v-if="errorMessage" class="error">{{ errorMessage }}</p><button class="primary wide" :disabled="busy">{{ busy ? '请稍候……' : mode === 'login' ? '进入阅读桌' : '创建我的词汇本' }}</button>
      </form>
    </section>

    <section v-else class="workspace">
      <header class="topbar"><div class="brand"><span class="brand-mark">✣</span><span>centipede</span><small>阅读收词</small></div><div class="account"><span>{{ user?.display_name || user?.email }}</span><button class="quiet" @click="signOut()">离开</button></div></header>
      <div class="welcome-row"><div><p class="eyebrow">你的阅读桌</p><h1>今天读到哪里？</h1></div><p class="date-note">{{ new Date().toLocaleDateString('zh-CN', { month: 'long', day: 'numeric', weekday: 'long' }) }}</p></div>

      <div class="desk-grid">
        <section class="reading-panel panel"><div class="panel-heading"><div><span class="section-number">01</span><h2>正在阅读</h2></div><button class="quiet" @click="captureSelection">带入选中文字 ↗</button></div><textarea ref="readingInput" v-model="readingText" class="reading-area" placeholder="把正在读的段落贴在这里……&#10;&#10;选中一个词，再点击右侧的“带入选中文字”，它会带着上下文一起被记下来。"></textarea><div class="reading-footer"><span>上下文会和词一起保存</span><span>{{ readingText.length }} 字</span></div></section>
        <section class="capture-panel panel"><div class="panel-heading"><div><span class="section-number">02</span><h2>快速收词</h2></div><span class="ink-dot"></span></div>
          <div class="capture-grid"><label class="language-field">语言<select v-model="entryForm.language"><option value="en">English · en</option><option value="de">Deutsch · de</option><option value="fr">Français · fr</option><option value="es">Español · es</option><option value="ja">日本語 · ja</option><option value="zh-Hans">中文 · zh-Hans</option></select></label><label>词 / 短语<input v-model="entryForm.text" placeholder="例如: sonderbar" required /></label><label>释义<input v-model="entryForm.definition" placeholder="它在这里是什么意思？" /></label><label>词元（可选）<input v-model="entryForm.lemma" placeholder="例如: sonderbar" /></label></div>
          <details><summary>来源与备注</summary><div class="source-grid"><input v-model="entryForm.book" placeholder="书名" /><input v-model="entryForm.chapter" placeholder="章节" /><input v-model="entryForm.location" placeholder="页码 / 位置" /><input v-model="entryForm.notes" placeholder="留一句备注" /></div></details><p v-if="errorMessage" class="error">{{ errorMessage }}</p><p v-if="notice" class="notice">{{ notice }}</p><button class="primary wide" :disabled="busy" @click="saveEntry">{{ busy ? '保存中……' : '收入词汇本 +' }}</button>
        </section>
      </div>

      <section class="vocabulary-section"><div class="list-heading"><div><p class="eyebrow">你的词汇本</p><h2>最近留下的词</h2></div><div class="filters"><input v-model="search" placeholder="搜索词或释义" @keyup.enter="loadEntries" /><select v-model="languageFilter" @change="loadEntries"><option value="">所有语言</option><option v-for="language in languageOptions" :key="language" :value="language">{{ language }}</option></select><button class="quiet" @click="loadEntries">刷新</button></div></div>
        <div v-if="entries.length" class="entry-list"><article v-for="entry in entries" :key="entry.id" class="entry-card"><div class="entry-main"><div class="entry-meta"><span class="language-tag">{{ entry.language }}</span><span>{{ entry.source.book || '未注明来源' }}<template v-if="entry.source.chapter"> · {{ entry.source.chapter }}</template></span></div><h3>{{ entry.text }}</h3><p v-if="entry.lemma" class="lemma">{{ entry.lemma }}</p><p class="definition">{{ entry.definition || '还没有写下释义' }}</p><blockquote v-if="entry.contexts[0]">“{{ entry.contexts[0].text }}”</blockquote></div><div class="entry-actions"><span class="status" :class="`status-${entry.status}`">{{ statusLabel(entry.status) }}</span><button class="status-button" @click="updateStatus(entry, entry.status === 'known' ? 'learning' : entry.status === 'new' ? 'learning' : 'known')">{{ entry.status === 'known' ? '重新学习' : entry.status === 'new' ? '开始学习' : '标记掌握' }}</button></div></article></div><div v-else class="empty-state"><span>∴</span><p>词汇本还是空白的。<br />把左边正在读的句子，变成明天还记得的词。</p></div>
      </section>
    </section>
  </main>
</template>
