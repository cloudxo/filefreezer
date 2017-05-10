// Copyright 2017, Timothy Bogdala <tdb@animal-machine.com>
// See the LICENSE file for more details.

package models

import "github.com/tbogdala/filefreezer"

// AllFilesGetResponse is the JSON serializable response given by the
// /api/files GET handlder.
type AllFilesGetResponse struct {
	Files []filefreezer.FileInfo
}

// FileGetResponse is the JSON serializable response given by the
// /api/file/{id} GET handlder.
type FileGetResponse struct {
	filefreezer.FileInfo
	MissingChunks []int
}

// FileGetByNameRequest is the JSON structure to be sent to the
// /api/file/name GET handler.
type FileGetByNameRequest struct {
	FileName string
}

// FileChunkPutResponse is the JSON serializable response given by the
// /api/chunk/{id}/{chunknum} PUT handlder.
type FileChunkPutResponse struct {
	Status bool
}

// FileChunksGetResponse is the JSON serializable response given by the
// /api/chunk/{fileid}/ GET handlder.
type FileChunksGetResponse struct {
	Chunks []filefreezer.FileChunk
}

// FileChunkGetResponse is the JSON serializable response given by the
// /api/chunk/{fileid}/{chunknumber} GET handlder.
type FileChunkGetResponse struct {
	Chunk filefreezer.FileChunk
}

// FilePutResponse is the JSON serializable response given by the
// /api/files PUT handlder.
type FilePutResponse struct {
	FileID int
}

// FilePutResponse is the JSON serializable request object sent to the
// /api/files PUT handlder.
type FilePutRequest struct {
	FileName   string
	LastMod    int64
	ChunkCount int
	FileHash   string
}

// FileDeleteRequest is the JSON serializable request object sent to the
// /api/files/{id} DELETE handlder.
type FileDeleteRequest struct {
	FileID int
}

// FileDeleteResponse is the JSON serializable response object from
// /api/file/{id} DELETE handler.
type FileDeleteResponse struct {
	Success bool
}
