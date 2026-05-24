<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page -->
  <div
    v-else
    class="relative grid min-h-screen overflow-hidden bg-[#f7f7f5] text-gray-950 dark:bg-[#090909] dark:text-white"
  >
    <div
      class="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_center,rgba(255,255,255,0.96)_0,rgba(247,247,245,0.84)_34%,rgba(229,231,229,0.72)_100%)] dark:bg-[radial-gradient(circle_at_center,rgba(38,38,38,0.82)_0,rgba(9,9,9,0.94)_55%,rgba(0,0,0,1)_100%)]"
    ></div>

    <div class="relative z-10 grid min-h-screen grid-rows-[auto_1fr_auto] px-5 py-6 sm:px-8 lg:px-12">
      <!-- Header -->
      <header class="relative mx-auto h-9 w-full max-w-6xl">
        <div
          class="absolute left-0 top-0 hidden max-w-[48vw] items-center rounded-lg border border-gray-200 bg-white/70 px-3 py-2 text-xs font-medium text-gray-700 shadow-sm backdrop-blur sm:inline-flex dark:border-dark-700 dark:bg-dark-900/70 dark:text-dark-300"
        >
          <span>{{ localText("开始使用", "Start with") }}</span>
          <span class="brand-serif ml-1 truncate text-gray-950 dark:text-white">{{ siteName }}</span>
        </div>

        <router-link to="/home" class="absolute left-0 top-0 flex h-9 min-w-[112px] items-center text-xl font-bold text-black dark:text-white sm:hidden">
          <BrandWordmark :name="siteName" />
        </router-link>

        <div class="absolute right-0 top-0 flex items-center gap-2">
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex h-9 items-center gap-1.5 rounded-lg bg-black px-3 text-xs font-semibold text-white transition-colors hover:bg-gray-800 dark:bg-white dark:text-black dark:hover:bg-gray-200"
          >
            <span>{{ t('home.dashboard') }}</span>
            <Icon name="arrowRight" size="xs" :stroke-width="2" />
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex h-9 items-center rounded-lg bg-black px-3 text-xs font-semibold text-white transition-colors hover:bg-gray-800 dark:bg-white dark:text-black dark:hover:bg-gray-200"
          >
            {{ t('home.login') }}
          </router-link>

          <LocaleSwitcher />

          <button
            @click="toggleTheme"
            class="hidden h-9 w-9 items-center justify-center rounded-lg text-gray-600 transition-colors hover:bg-black/5 hover:text-gray-950 dark:text-dark-300 dark:hover:bg-white/10 dark:hover:text-white sm:inline-flex"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="sm" />
            <Icon v-else name="moon" size="sm" />
          </button>
        </div>
      </header>

      <!-- Brand Center -->
      <main class="mx-auto flex w-full max-w-6xl items-center justify-center py-14 text-center">
        <section class="flex flex-col items-center">
          <h1
            class="brand-serif text-[44px] font-semibold leading-none tracking-normal text-black sm:text-6xl lg:text-7xl dark:text-white"
          >
            {{ siteName }}
          </h1>
          <p class="mt-4 max-w-xl text-sm leading-6 text-gray-500 sm:text-base dark:text-dark-300">
            {{ siteSubtitle }}
          </p>
        </section>
      </main>

      <!-- Entry Links -->
      <nav class="mx-auto grid w-full max-w-4xl grid-cols-1 gap-4 pb-8 sm:grid-cols-2 lg:grid-cols-4">
        <router-link
          v-for="item in homeLinks"
          :key="item.title"
          :to="item.to"
          class="group min-h-[112px] rounded-lg px-1 py-2 transition-colors hover:bg-black/[0.03] dark:hover:bg-white/[0.06] sm:px-3"
        >
          <div class="flex items-center gap-2 text-[19px] font-semibold leading-6 text-black dark:text-white">
            <span>{{ item.title }}</span>
            <Icon
              name="arrowRight"
              size="sm"
              :stroke-width="2"
              class="transition-transform group-hover:translate-x-0.5"
            />
          </div>
          <p class="mt-3 text-sm leading-5 text-gray-500 dark:text-dark-400">
            {{ item.desc }}
          </p>
        </router-link>
      </nav>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import BrandWordmark from '@/components/common/BrandWordmark.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import { resolveSiteName } from '@/constants/site'

const { t, locale } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => resolveSiteName(appStore.cachedPublicSettings?.site_name || appStore.siteName))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform')
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => isAdmin.value ? '/admin/dashboard' : '/dashboard')

function localText(zh: string, en: string): string {
  return locale.value.startsWith('zh') ? zh : en
}

const homeLinks = computed(() => {
  const loginPath = '/login'

  return [
    {
      title: localText('控制台', 'Dashboard'),
      desc: localText('查看账户状态、用量和关键配置。', 'View account status, usage, and key settings.'),
      to: isAuthenticated.value ? dashboardPath.value : loginPath,
    },
    {
      title: localText('API 密钥', 'API Keys'),
      desc: localText('创建、管理并复制你的服务密钥。', 'Create, manage, and copy your service keys.'),
      to: isAuthenticated.value ? '/keys' : loginPath,
    },
    {
      title: localText('充值订阅', 'Subscribe'),
      desc: localText('选择套餐或为账户余额充值。', 'Choose a plan or recharge your account balance.'),
      to: isAuthenticated.value ? '/purchase' : loginPath,
    },
    {
      title: localText('使用统计', 'Usage'),
      desc: localText('追踪请求、Token 消耗和调用成本。', 'Track requests, token usage, and call costs.'),
      to: isAuthenticated.value ? '/usage' : loginPath,
    },
  ]
})

// Toggle theme
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Initialize theme
function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

onMounted(() => {
  initTheme()

  // Check auth state
  authStore.checkAuth()

  // Ensure public settings are loaded (will use cache if already loaded from injected config)
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})
</script>
