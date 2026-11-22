import { channelLogoPath } from '@/lib/channelLogo'
import { useAuthedImageUrl } from '@/lib/storedImage'

// A channel's uploaded logo, fetched with the session token (the route is not
// public) and drawn at whatever box the caller gives it. Renders nothing when
// the channel has no logo, so callers can lay out around it: the grid's channel
// column simply shows the number and the name then.
export function ChannelLogoImage({
  imageId,
  name,
  className,
}: {
  imageId: string | null | undefined
  name: string
  className?: string
}) {
  const url = useAuthedImageUrl(channelLogoPath(imageId))
  if (!url) return null
  return <img src={url} alt={`${name} logo`} className={className} />
}
