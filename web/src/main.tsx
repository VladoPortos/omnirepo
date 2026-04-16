import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';

function App() {
  return <div className="min-h-screen bg-background text-foreground">OmniRepo</div>;
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
