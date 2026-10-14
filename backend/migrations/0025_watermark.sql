-- Client-rendered watermark of a channel and of a block (the "Deco" layer of
-- the guide, see docs/ARCHITECTURE.md "Watermark").
--
-- One row per owner (a channel or a block); no row means "inherit" for a block
-- and "no watermark" for a channel. owner_id is deliberately not a foreign key
-- (it points at channel or block, per owner_type): the REST handlers that
-- delete an owner delete its row with it.
--
-- mode: inherit (use the channel's), disable (no watermark while this owner's
-- programs and the filler that decorates them are on air) or override (this
-- row's own image and options). A channel's inherit and disable both mean no
-- watermark.
--
-- image_id is regenerated on every upload: it is the immutable identity of the
-- bytes and the path the client downloads (GET /watermarks/{image_id}/image),
-- so a cached copy never needs revalidating. The image is validated by sniffing
-- (PNG/WebP/JPEG only) and capped before it is stored.
CREATE TABLE watermark (
    tenant_id    TEXT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    owner_type   TEXT NOT NULL CHECK (owner_type IN ('channel', 'block')),
    owner_id     TEXT NOT NULL,
    mode         TEXT NOT NULL CHECK (mode IN ('inherit', 'disable', 'override')),
    image_id     TEXT,
    content_type TEXT,
    image        BLOB,
    position     TEXT NOT NULL DEFAULT 'bottom_right' CHECK (position IN ('top_left', 'top_right', 'bottom_left', 'bottom_right')),
    opacity_pct  INTEGER NOT NULL DEFAULT 70 CHECK (opacity_pct BETWEEN 0 AND 100),
    size_pct     INTEGER NOT NULL DEFAULT 8 CHECK (size_pct BETWEEN 2 AND 40),
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, owner_type, owner_id)
);
CREATE UNIQUE INDEX idx_watermark_image ON watermark(image_id) WHERE image_id IS NOT NULL;
