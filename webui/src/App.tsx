import { Navigate, Route, Routes } from 'react-router-dom'

import { Layout } from '@/components/Layout'
import { RequireAuth } from '@/components/RequireAuth'
import { BlocksPage } from '@/pages/BlocksPage'
import { ChannelsPage } from '@/pages/ChannelsPage'
import { DevicesPage } from '@/pages/DevicesPage'
import { EpgPage } from '@/pages/EpgPage'
import { FillerPoolsPage } from '@/pages/FillerPoolsPage'
import { FillerRulesPage } from '@/pages/FillerRulesPage'
import { LoginPage } from '@/pages/LoginPage'
import { MediaSourcesPage } from '@/pages/MediaSourcesPage'
import { SchedulePage } from '@/pages/SchedulePage'

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route path="/" element={<EpgPage />} />
        <Route path="/channels" element={<ChannelsPage />} />
        <Route path="/blocks" element={<BlocksPage />} />
        <Route path="/schedule" element={<SchedulePage />} />
        <Route path="/filler-pools" element={<FillerPoolsPage />} />
        <Route path="/filler-rules" element={<FillerRulesPage />} />
        <Route path="/media-sources" element={<MediaSourcesPage />} />
        <Route path="/devices" element={<DevicesPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
