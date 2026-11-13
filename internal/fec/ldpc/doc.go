// Package ldpc implements the inner code of docs/FORMAT.md section 8: the IEEE 802.11n
// quasi-cyclic LDPC code of length 1944, shortened so that one codeword carries one shard and its
// CRC-32C, with a layered normalised min-sum soft-decision decoder. Its codes satisfy
// fec.InnerCode.
package ldpc
