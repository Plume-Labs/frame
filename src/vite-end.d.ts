/// <reference types="vite/client" />
declare const GITHUB_RUNTIME_PERMANENT_NAME: string
declare const BASE_KV_SERVICE_URL: string

// lucide-react v0.484 ships JS files for deep icon imports without bundled .d.ts
// sidecars. Declare them so TypeScript accepts the generated shadcn/ui imports.
declare module 'lucide-react/dist/esm/icons/*'
