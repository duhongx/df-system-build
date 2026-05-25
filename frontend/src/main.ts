import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import {
  ArrowDown,
  ArrowLeft,
  Bell,
  Box,
  Check,
  CircleCheck,
  CircleClose,
  Clock,
  Close,
  Cloudy,
  Coin,
  Connection,
  Cpu,
  DataLine,
  Delete,
  Document,
  Expand,
  Files,
  Fold,
  Folder,
  FolderAdd,
  Grid,
  InfoFilled,
  Loading,
  Lock,
  Monitor,
  Moon,
  Platform,
  Plus,
  Refresh,
  Search,
  Setting,
  Sunny,
  SwitchButton,
  Top,
  Upload,
  User,
  VideoPlay,
  WarningFilled,
} from '@element-plus/icons-vue'
import App from './App.vue'
import router from './router'
import './styles/index.css'

const icons = {
  ArrowDown,
  ArrowLeft,
  Bell,
  Box,
  Check,
  CircleCheck,
  CircleClose,
  Clock,
  Close,
  Cloudy,
  Coin,
  Connection,
  Cpu,
  DataLine,
  Delete,
  Document,
  Expand,
  Files,
  Fold,
  Folder,
  FolderAdd,
  Grid,
  InfoFilled,
  Loading,
  Lock,
  Monitor,
  Moon,
  Platform,
  Plus,
  Refresh,
  Search,
  Setting,
  Sunny,
  SwitchButton,
  Top,
  Upload,
  User,
  VideoPlay,
  WarningFilled,
}

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(ElementPlus)
for (const [key, component] of Object.entries(icons)) {
  app.component(key, component)
}
app.mount('#app')
