import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "./index.css"
import App from "./App.tsx"
import { Toaster } from "sonner"
import { TooltipProvider } from "@/components/ui/tooltip"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TooltipProvider>
      <App />
      <Toaster
        theme="dark"
        position="bottom-right"
        toastOptions={{
          style: {
            background: "#131317",
            border: "1px solid #27272A",
            color: "#FAFAFA",
            fontSize: "12px",
          },
        }}
      />
    </TooltipProvider>
  </StrictMode>
)
