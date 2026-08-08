// Package media indexes media attachments associated with messages.
//
// Indexer is the entry point (NewIndexer); it records caller-supplied attachment
// metadata (deriving only the media_type code from the MIME type) into media_index
// rows within a caller-supplied transaction, so media can later be listed and
// queried per channel.
package media
