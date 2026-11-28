package db

// How a wait for sandbox room ended, as room_waits.ended stores it and its
// CHECK names them. Written by hand beside the generated code, which sqlc
// leaves alone, so every package that ends a wait spells it once.
const (
	RoomWaitRoom       = "room"
	RoomWaitGaveUp     = "gave_up"
	RoomWaitSuperseded = "superseded"
	RoomWaitCancelled  = "cancelled"
)
