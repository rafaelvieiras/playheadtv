import { Navigate, createBrowserRouter } from 'react-router-dom'

import { Layout } from '@/components/Layout'
import { RequireAuth } from '@/components/RequireAuth'
import { BlockEditorPage } from '@/pages/BlockEditorPage'
import { BlocksPage } from '@/pages/BlocksPage'
import { ChannelPage } from '@/pages/ChannelPage'
import { ChannelsPage } from '@/pages/ChannelsPage'
import { CollectionEditorPage } from '@/pages/CollectionEditorPage'
import { CollectionsPage } from '@/pages/CollectionsPage'
import { DevicesPage } from '@/pages/DevicesPage'
import { EpgPage } from '@/pages/EpgPage'
import { BreakTemplateEditorPage } from '@/pages/BreakTemplateEditorPage'
import { BreakTemplatesPage } from '@/pages/BreakTemplatesPage'
import { CutRuleSetEditorPage } from '@/pages/CutRuleSetEditorPage'
import { CutRuleSetsPage } from '@/pages/CutRuleSetsPage'
import { FillerPoolEditorPage } from '@/pages/FillerPoolEditorPage'
import { FillerPoolsPage } from '@/pages/FillerPoolsPage'
import { FillerRuleEditorPage } from '@/pages/FillerRuleEditorPage'
import { FillerRulesPage } from '@/pages/FillerRulesPage'
import { LoginPage } from '@/pages/LoginPage'
import { ResetPasswordPage } from '@/pages/ResetPasswordPage'
import { MediaSourcesPage } from '@/pages/MediaSourcesPage'
import { PluginsPage } from '@/pages/PluginsPage'
import { StreamSelectorPage } from '@/pages/StreamSelectorPage'

// A data router, not `<BrowserRouter>`: the editors guard a dirty form with
// `useBlocker`, which only exists on a data router (see
// components/SaveBar.tsx and docs/ARCHITECTURE.md "Admin shell"). The SPA is
// served under /admin/ by the Go binary, so the basename lives here.
export const router = createBrowserRouter(
  [
    { path: '/login', element: <LoginPage /> },
    { path: '/reset-password', element: <ResetPasswordPage /> },
    {
      element: (
        <RequireAuth>
          <Layout />
        </RequireAuth>
      ),
      children: [
        { path: '/', element: <EpgPage /> },
        { path: '/channels', element: <ChannelsPage /> },
        { path: '/channels/:id', element: <ChannelPage /> },
        { path: '/blocks', element: <BlocksPage /> },
        { path: '/blocks/:id', element: <BlockEditorPage /> },
        { path: '/collections', element: <CollectionsPage /> },
        { path: '/collections/:id', element: <CollectionEditorPage /> },
        { path: '/filler-pools', element: <FillerPoolsPage /> },
        { path: '/filler-pools/:id', element: <FillerPoolEditorPage /> },
        { path: '/filler-rules', element: <FillerRulesPage /> },
        { path: '/filler-rules/:id', element: <FillerRuleEditorPage /> },
        { path: '/break-templates', element: <BreakTemplatesPage /> },
        { path: '/break-templates/:id', element: <BreakTemplateEditorPage /> },
        { path: '/cut-rule-sets', element: <CutRuleSetsPage /> },
        { path: '/cut-rule-sets/:id', element: <CutRuleSetEditorPage /> },
        { path: '/stream-selector', element: <StreamSelectorPage /> },
        { path: '/media-sources', element: <MediaSourcesPage /> },
        { path: '/devices', element: <DevicesPage /> },
        { path: '/plugins', element: <PluginsPage /> },
      ],
    },
    { path: '*', element: <Navigate to="/" replace /> },
  ],
  { basename: '/admin' },
)
