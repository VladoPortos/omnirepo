import React from 'react';
import ReactDOM from 'react-dom/client';
import { RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { router } from './App';
import { Toaster } from '@/components/ui/sonner';
import './index.css';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: 1 },
  },
});

// Apply dark mode by default
if (!localStorage.getItem('omnirepo-theme')) {
  document.documentElement.classList.add('dark');
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
      {/* Toast host: without this, every toast.success/error call across
          the app is a silent no-op (F-02.1). Mounted at the top level so
          toasts persist across route transitions. */}
      <Toaster richColors position="top-right" />
    </QueryClientProvider>
  </React.StrictMode>,
);
