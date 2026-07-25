import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import App from './App'
import './styles.css'

// 全局 QueryClient 统一控制 REST 数据的新鲜度、窗口聚焦行为和失败重试次数。
const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 2_000, refetchOnWindowFocus: false, retry: 1 } },
})

// React 严格模式用于开发期发现副作用问题；QueryClientProvider 向所有页面
// 注入请求缓存，App 则负责连接 Wails 桥接服务和本地 daemon。
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
