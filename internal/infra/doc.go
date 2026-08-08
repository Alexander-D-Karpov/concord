// Package infra provides Snowflake ID generation and is the root for the db,
// cache, and migrations subpackages.
//
// SnowflakeGenerator produces 64-bit, time-ordered IDs (custom epoch 2022-01-01;
// 41-bit millisecond timestamp, 10-bit worker ID, 12-bit sequence). Generate is
// lock-serialized and spin-waits when the per-millisecond sequence overflows.
// Because IDs are time-sortable, pagination and read-tracking rely on their order.
package infra
