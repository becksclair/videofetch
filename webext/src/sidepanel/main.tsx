import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '@/styles.css';
import { SidepanelApp } from './SidepanelApp';

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <SidepanelApp />
  </StrictMode>
);
