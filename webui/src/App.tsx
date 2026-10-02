import { Navigate, Route, Routes } from 'react-router-dom'

import { Layout } from '@/components/Layout'
import { RequireAuth } from '@/components/RequireAuth'
import { BlockEditorPage } from '@/pages/BlockEditorPage'
import { BlocksPage } from '@/pages/BlocksPage'
import { ChannelPage } from '@/pages/ChannelPage'
import { ChannelsPage } from '@/pages/ChannelsPage'
import { DevicesPage } from '@/pages/DevicesPage'
import { EpgPage } from '@/pages/EpgPage'
import { FillerPoolEditorPage } from '@/pages/FillerPoolEditorPage'
import { FillerPoolsPage } from '@/pages/FillerPoolsPage'
import { FillerRuleEditorPage } from '@/pages/FillerRuleEditorPage'
import { FillerRulesPage } from '@/pages/FillerRulesPage'
import { LoginPage } from '@/pages/LoginPage'
import { MediaSourcesPage } from '@/pages/MediaSourcesPage'
import { StreamSelectorPage } from '@/pages/StreamSelectorPage'

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
        <Route path="/channels/:id" element={<ChannelPage />} />
        <Route path="/blocks" element={<BlocksPage />} />
        <Route
          path="/blocks/:id"
          element={<BlockEditorPage />}
        />
        <Route path="/filler-pools" element={<FillerPoolsPage />} />
        <Route path="/filler-pools/:id" element={<FillerPoolEditorPage />} />
        <Route path="/filler-rules" element={<FillerRulesPage />} />
        <Route path="/filler-rules/:id" element={<FillerRuleEditorPage />} />
        <Route path="/stream-selector" element={<StreamSelectorPage />} />
        <Route path="/media-sources" element={<MediaSourcesPage />} />
        <Route path="/devices" element={<DevicesPage />} />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
