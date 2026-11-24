import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams, useSearchParams } from 'react-router-dom'

import { ChannelGuide } from '@/components/ChannelGuide'
import { ChannelDeco } from '@/components/ChannelDeco'
import { ChannelFillerRules } from '@/components/ChannelFillerRules'
import { ChannelProgramming, CreateScheduleButton } from '@/components/ChannelProgramming'
import { ChannelSettings } from '@/components/ChannelSettings'
import { InlineWarning } from '@/components/InlineWarning'
import { PageHeader } from '@/components/PageHeader'
import { ResetGridDialog } from '@/components/ResetGridDialog'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { api } from '@/lib/http'
import type { Channel } from '@/lib/types'

const TABS = ['programming', 'fillers', 'guide', 'deco', 'settings'] as const
type TabName = (typeof TABS)[number]

// The authoring hub of one channel. Panels stay mounted (hidden when
// inactive) so an unsaved Settings draft survives switching tabs.
export function ChannelPage() {
  const { id = '' } = useParams()
  const [searchParams, setSearchParams] = useSearchParams()
  const requested = searchParams.get('tab')
  const tab: TabName = TABS.find((t) => t === requested) ?? 'programming'

  const { data: channel, isLoading, isError } = useQuery({
    queryKey: ['channels', id],
    queryFn: () => api.get<Channel>(`/channels/${id}`),
    enabled: !!id,
  })
  const { data: channels } = useQuery({ queryKey: ['channels'], queryFn: () => api.get<Channel[]>('/channels') })
  const [resetting, setResetting] = useState<Channel | undefined>(undefined)

  function setTab(next: string) {
    setSearchParams(next === 'programming' ? {} : { tab: next }, { replace: true })
  }

  const hidden = 'data-[state=inactive]:hidden'

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={channel ? `${channel.number} — ${channel.name}` : 'Channel'}
        breadcrumb={[{ label: 'Channels', to: '/channels' }, { label: channel?.name ?? 'Channel' }]}
        actions={
          channel && (
            <Button variant="outline" size="sm" data-testid="reset-grid" onClick={() => setResetting(channel)}>
              Reset grid
            </Button>
          )
        }
      />
      {isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {isError && <p className="text-sm text-destructive">Could not load the channel.</p>}
      {channel && (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="programming">Programming</TabsTrigger>
            <TabsTrigger value="fillers">Fillers</TabsTrigger>
            <TabsTrigger value="guide">Guide</TabsTrigger>
            <TabsTrigger value="deco">Deco</TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
          <TabsContent value="programming" forceMount className={hidden}>
            <ChannelProgramming channel={channel} channels={channels ?? []} onGoToSettings={() => setTab('settings')} />
          </TabsContent>
          <TabsContent value="fillers" forceMount className={hidden}>
            <ChannelFillerRules channelId={channel.id} />
          </TabsContent>
          <TabsContent value="guide" forceMount className={hidden}>
            {/* A channel without a schedule has no grid to draw: what a stored
                grid would show under it belongs to a schedule that is gone, and
                reads as real programming on a channel that has none. Say it,
                and offer the action that creates one. */}
            {channel.schedule_id ? (
              <ChannelGuide channel={channel} active={tab === 'guide'} />
            ) : (
              <div className="flex flex-col items-start gap-3" data-testid="guide-no-schedule">
                <InlineWarning title="This channel has no schedule">
                  Nothing is programmed on this channel, so there is nothing to draw here. Create a schedule and the
                  grid fills in once the backend has expanded it.
                </InlineWarning>
                <div className="flex flex-wrap items-center gap-2">
                  <CreateScheduleButton channel={channel} testId="create-schedule-guide" />
                  <Button variant="outline" size="sm" onClick={() => setTab('programming')}>
                    Open Programming
                  </Button>
                </div>
              </div>
            )}
          </TabsContent>
          <TabsContent value="deco" forceMount className={hidden}>
            {/* Remounted after a channel save so the base schedule draft resets. */}
            <ChannelDeco key={`${channel.id}-${channel.updated_at}`} channel={channel} />
          </TabsContent>
          <TabsContent value="settings" forceMount className={hidden}>
            {/* Remounted after a save (updated_at changes) so the draft resets. */}
            <ChannelSettings
              key={`${channel.id}-${channel.updated_at}`}
              channel={channel}
              channels={channels ?? []}
              onGoToDeco={() => setTab('deco')}
            />
          </TabsContent>
        </Tabs>
      )}
      <ResetGridDialog channel={resetting} onClose={() => setResetting(undefined)} />
    </div>
  )
}
