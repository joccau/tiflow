// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package relay

import (
	"bytes"
	"os"
	"path"
	"path/filepath"
	"time"

	gmysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/pingcap/check"

	"github.com/pingcap/tiflow/dm/pkg/binlog/event"
	"github.com/pingcap/tiflow/dm/pkg/gtid"
	"github.com/pingcap/tiflow/dm/pkg/log"
)

var _ = check.Suite(&testFileWriterSuite{})

type testFileWriterSuite struct{}

func (t *testFileWriterSuite) TestInterfaceMethods(c *check.C) {
	var (
		relayDir = c.MkDir()
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		filename = "test-mysql-bin.000001"
		header   = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
			Flags:     0x01,
		}
		latestPos uint32 = 4
		ev, _            = event.GenFormatDescriptionEvent(header, latestPos)
	)

	c.Assert(os.MkdirAll(path.Join(relayDir, uuid), 0o755), check.IsNil)

	w := NewFileWriter(log.L(), relayDir)
	c.Assert(w, check.NotNil)

	// not prepared
	_, err := w.WriteEvent(ev)
	c.Assert(err, check.ErrorMatches, ".*not valid.*")

	w.Init(uuid, filename)

	// write event
	res, err := w.WriteEvent(ev)
	c.Assert(err, check.IsNil)
	c.Assert(res.Ignore, check.IsFalse)

	// close the writer
	c.Assert(w.Close(), check.IsNil)
}

func (t *testFileWriterSuite) TestRelayDir(c *check.C) {
	var (
		relayDir = c.MkDir()
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		header   = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
			Flags:     0x01,
		}
		latestPos uint32 = 4
	)
	ev, err := event.GenFormatDescriptionEvent(header, latestPos)
	c.Assert(err, check.IsNil)

	// not inited
	w1 := NewFileWriter(log.L(), relayDir)
	defer w1.Close()
	_, err = w1.WriteEvent(ev)
	c.Assert(err, check.ErrorMatches, ".*not valid.*")

	// invalid dir
	w2 := NewFileWriter(log.L(), relayDir)
	defer w2.Close()
	w2.Init("invalid\x00uuid", "bin.000001")
	_, err = w2.WriteEvent(ev)
	c.Assert(err, check.ErrorMatches, ".*invalid argument.*")

	// valid directory, but no filename specified
	w3 := NewFileWriter(log.L(), relayDir)
	defer w3.Close()
	w3.Init(uuid, "")
	_, err = w3.WriteEvent(ev)
	c.Assert(err, check.ErrorMatches, ".*not valid.*")

	// valid directory, but invalid filename
	w4 := NewFileWriter(log.L(), relayDir)
	defer w4.Close()
	w4.Init(uuid, "test-mysql-bin.666abc")
	_, err = w4.WriteEvent(ev)
	c.Assert(err, check.ErrorMatches, ".*not valid.*")

	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)

	// valid directory, valid filename
	w5 := NewFileWriter(log.L(), relayDir)
	defer w5.Close()
	w5.Init(uuid, "test-mysql-bin.000001")
	result, err := w5.WriteEvent(ev)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)
}

func (t *testFileWriterSuite) TestFormatDescriptionEvent(c *check.C) {
	var (
		relayDir = c.MkDir()
		filename = "test-mysql-bin.000001"
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		header   = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
			Flags:     0x01,
		}
		latestPos uint32 = 4
	)
	formatDescEv, err := event.GenFormatDescriptionEvent(header, latestPos)
	c.Assert(err, check.IsNil)
	c.Assert(os.Mkdir(path.Join(relayDir, uuid), 0o755), check.IsNil)

	// write FormatDescriptionEvent to empty file
	w := NewFileWriter(log.L(), relayDir)
	defer w.Close()
	w.Init(uuid, filename)
	result, err := w.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)
	fileSize := int64(len(replication.BinLogFileHeader) + len(formatDescEv.RawData))
	t.verifyFilenameOffset(c, w, filename, fileSize)
	latestPos = formatDescEv.Header.LogPos

	// write FormatDescriptionEvent again, ignore
	result, err = w.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsTrue)
	c.Assert(result.IgnoreReason, check.Equals, ignoreReasonAlreadyExists)
	t.verifyFilenameOffset(c, w, filename, fileSize)

	// write another event
	queryEv, err := event.GenQueryEvent(header, latestPos, 0, 0, 0, nil, []byte("schema"), []byte("BEGIN"))
	c.Assert(err, check.IsNil)
	result, err = w.WriteEvent(queryEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)
	fileSize += int64(len(queryEv.RawData))
	t.verifyFilenameOffset(c, w, filename, fileSize)

	// write FormatDescriptionEvent again, ignore
	result, err = w.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsTrue)
	c.Assert(result.IgnoreReason, check.Equals, ignoreReasonAlreadyExists)
	t.verifyFilenameOffset(c, w, filename, fileSize)

	// check events by reading them back
	events := make([]*replication.BinlogEvent, 0, 2)
	count := 0
	onEventFunc := func(e *replication.BinlogEvent) error {
		count++
		if count > 2 {
			c.Fatalf("too many events received, %+v", e.Header)
		}
		events = append(events, e)
		return nil
	}
	fullName := filepath.Join(relayDir, uuid, filename)
	err = replication.NewBinlogParser().ParseFile(fullName, 0, onEventFunc)
	c.Assert(err, check.IsNil)
	c.Assert(events, check.HasLen, 2)
	c.Assert(events[0], check.DeepEquals, formatDescEv)
	c.Assert(events[1], check.DeepEquals, queryEv)
}

func (t *testFileWriterSuite) verifyFilenameOffset(c *check.C, w Writer, filename string, offset int64) {
	wf, ok := w.(*FileWriter)
	c.Assert(ok, check.IsTrue)
	c.Assert(wf.filename.Load(), check.Equals, filename)
	c.Assert(wf.offset(), check.Equals, offset)
}

func (t *testFileWriterSuite) TestRotateEventWithFormatDescriptionEvent(c *check.C) {
	var (
		relayDir            = c.MkDir()
		uuid                = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		filename            = "test-mysql-bin.000001"
		nextFilename        = "test-mysql-bin.000002"
		nextFilePos  uint64 = 4
		header              = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
			Flags:     0x01,
		}
		fakeHeader = &replication.EventHeader{
			Timestamp: 0, // mark as fake
			ServerID:  11,
			Flags:     0x01,
		}
		latestPos uint32 = 4
	)

	formatDescEv, err := event.GenFormatDescriptionEvent(header, latestPos)
	c.Assert(err, check.IsNil)
	c.Assert(formatDescEv, check.NotNil)
	latestPos = formatDescEv.Header.LogPos

	rotateEv, err := event.GenRotateEvent(header, latestPos, []byte(nextFilename), nextFilePos)
	c.Assert(err, check.IsNil)
	c.Assert(rotateEv, check.NotNil)

	fakeRotateEv, err := event.GenRotateEvent(fakeHeader, latestPos, []byte(nextFilename), nextFilePos)
	c.Assert(err, check.IsNil)
	c.Assert(fakeRotateEv, check.NotNil)

	// hole exists between formatDescEv and holeRotateEv, but the size is too small to fill
	holeRotateEv, err := event.GenRotateEvent(header, latestPos+event.MinUserVarEventLen-1, []byte(nextFilename), nextFilePos)
	c.Assert(err, check.IsNil)
	c.Assert(holeRotateEv, check.NotNil)

	// 1: non-fake RotateEvent before FormatDescriptionEvent, invalid
	w1 := NewFileWriter(log.L(), relayDir)
	defer w1.Close()
	w1.Init(uuid, filename)
	_, err = w1.WriteEvent(rotateEv)
	c.Assert(err, check.ErrorMatches, ".*file not opened.*")

	// 2. fake RotateEvent before FormatDescriptionEvent
	relayDir = c.MkDir() // use a new relay directory
	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)
	w2 := NewFileWriter(log.L(), relayDir)
	defer w2.Close()
	w2.Init(uuid, filename)
	result, err := w2.WriteEvent(fakeRotateEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsTrue) // ignore fake RotateEvent
	c.Assert(result.IgnoreReason, check.Equals, ignoreReasonFakeRotate)

	result, err = w2.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)

	fileSize := int64(len(replication.BinLogFileHeader) + len(formatDescEv.RawData))
	t.verifyFilenameOffset(c, w2, nextFilename, fileSize)

	// filename should be empty, next file should contain only one FormatDescriptionEvent
	filename1 := filepath.Join(relayDir, uuid, filename)
	filename2 := filepath.Join(relayDir, uuid, nextFilename)
	_, err = os.Stat(filename1)
	c.Assert(os.IsNotExist(err), check.IsTrue)
	data, err := os.ReadFile(filename2)
	c.Assert(err, check.IsNil)
	fileHeaderLen := len(replication.BinLogFileHeader)
	c.Assert(len(data), check.Equals, fileHeaderLen+len(formatDescEv.RawData))
	c.Assert(data[fileHeaderLen:], check.DeepEquals, formatDescEv.RawData)

	// 3. FormatDescriptionEvent before fake RotateEvent
	relayDir = c.MkDir() // use a new relay directory
	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)
	w3 := NewFileWriter(log.L(), relayDir)
	defer w3.Close()
	w3.Init(uuid, filename)
	result, err = w3.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result, check.NotNil)
	c.Assert(result.Ignore, check.IsFalse)

	result, err = w3.WriteEvent(fakeRotateEv)
	c.Assert(err, check.IsNil)
	c.Assert(result, check.NotNil)
	c.Assert(result.Ignore, check.IsTrue)
	c.Assert(result.IgnoreReason, check.Equals, ignoreReasonFakeRotate)

	t.verifyFilenameOffset(c, w3, nextFilename, fileSize)

	// filename should contain only one FormatDescriptionEvent, next file should be empty
	filename1 = filepath.Join(relayDir, uuid, filename)
	filename2 = filepath.Join(relayDir, uuid, nextFilename)
	_, err = os.Stat(filename2)
	c.Assert(os.IsNotExist(err), check.IsTrue)
	data, err = os.ReadFile(filename1)
	c.Assert(err, check.IsNil)
	c.Assert(len(data), check.Equals, fileHeaderLen+len(formatDescEv.RawData))
	c.Assert(data[fileHeaderLen:], check.DeepEquals, formatDescEv.RawData)

	// 4. FormatDescriptionEvent before non-fake RotateEvent
	relayDir = c.MkDir() // use a new relay directory
	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)
	w4 := NewFileWriter(log.L(), relayDir)
	defer w4.Close()
	w4.Init(uuid, filename)
	result, err = w4.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result, check.NotNil)
	c.Assert(result.Ignore, check.IsFalse)

	// try to write a rotateEv with hole exists
	_, err = w4.WriteEvent(holeRotateEv)
	c.Assert(err, check.ErrorMatches, ".*required dummy event size.*is too small.*")

	result, err = w4.WriteEvent(rotateEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)

	fileSize += int64(len(rotateEv.RawData))
	t.verifyFilenameOffset(c, w4, nextFilename, fileSize)

	// write again, duplicate, but we already rotated and new binlog file not created
	_, err = w4.WriteEvent(rotateEv)
	c.Assert(err, check.ErrorMatches, ".*(no such file or directory|The system cannot find the file specified).*")

	// filename should contain both one FormatDescriptionEvent and one RotateEvent, next file should be empty
	filename1 = filepath.Join(relayDir, uuid, filename)
	filename2 = filepath.Join(relayDir, uuid, nextFilename)
	_, err = os.Stat(filename2)
	c.Assert(os.IsNotExist(err), check.IsTrue)
	data, err = os.ReadFile(filename1)
	c.Assert(err, check.IsNil)
	c.Assert(len(data), check.Equals, fileHeaderLen+len(formatDescEv.RawData)+len(rotateEv.RawData))
	c.Assert(data[fileHeaderLen:fileHeaderLen+len(formatDescEv.RawData)], check.DeepEquals, formatDescEv.RawData)
	c.Assert(data[fileHeaderLen+len(formatDescEv.RawData):], check.DeepEquals, rotateEv.RawData)
}

func (t *testFileWriterSuite) TestWriteMultiEvents(c *check.C) {
	var (
		flavor                    = gmysql.MySQLFlavor
		serverID           uint32 = 11
		latestPos          uint32
		previousGTIDSetStr        = "3ccc475b-2343-11e7-be21-6c0b84d59f30:1-14,406a3f61-690d-11e7-87c5-6c92bf46f384:1-94321383,53bfca22-690d-11e7-8a62-18ded7a37b78:1-495,686e1ab6-c47e-11e7-a42c-6c92bf46f384:1-34981190,03fc0263-28c7-11e7-a653-6c0b84d59f30:1-7041423,05474d3c-28c7-11e7-8352-203db246dd3d:1-170,10b039fc-c843-11e7-8f6a-1866daf8d810:1-308290454"
		latestGTIDStr             = "3ccc475b-2343-11e7-be21-6c0b84d59f30:14"
		latestXID          uint64 = 10

		relayDir = c.MkDir()
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		filename = "test-mysql-bin.000001"
	)
	previousGTIDSet, err := gtid.ParserGTID(flavor, previousGTIDSetStr)
	c.Assert(err, check.IsNil)
	latestGTID, err := gtid.ParserGTID(flavor, latestGTIDStr)
	c.Assert(err, check.IsNil)

	// use a binlog event generator to generate some binlog events.
	allEvents := make([]*replication.BinlogEvent, 0, 10)
	var allData bytes.Buffer
	g, err := event.NewGenerator(flavor, serverID, latestPos, latestGTID, previousGTIDSet, latestXID)
	c.Assert(err, check.IsNil)

	// file header with FormatDescriptionEvent and PreviousGTIDsEvent
	events, data, err := g.GenFileHeader(0)
	c.Assert(err, check.IsNil)
	allEvents = append(allEvents, events...)
	allData.Write(data)

	// CREATE DATABASE/TABLE
	queries := []string{"CRATE DATABASE `db`", "CREATE TABLE `db`.`tbl` (c1 INT)"}
	for _, query := range queries {
		events, data, err = g.GenDDLEvents("db", query, 0)
		c.Assert(err, check.IsNil)
		allEvents = append(allEvents, events...)
		allData.Write(data)
	}

	// INSERT INTO `db`.`tbl` VALUES (1)
	var (
		tableID    uint64 = 8
		columnType        = []byte{gmysql.MYSQL_TYPE_LONG}
		insertRows        = make([][]interface{}, 1)
	)
	insertRows[0] = []interface{}{int32(1)}
	events, data, err = g.GenDMLEvents(replication.WRITE_ROWS_EVENTv2, []*event.DMLData{
		{TableID: tableID, Schema: "db", Table: "tbl", ColumnType: columnType, Rows: insertRows},
	}, 0)
	c.Assert(err, check.IsNil)
	allEvents = append(allEvents, events...)
	allData.Write(data)

	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)

	// write the events to the file
	w := NewFileWriter(log.L(), relayDir)
	w.Init(uuid, filename)
	for _, ev := range allEvents {
		result, err2 := w.WriteEvent(ev)
		c.Assert(err2, check.IsNil)
		c.Assert(result.Ignore, check.IsFalse) // no event is ignored
	}

	t.verifyFilenameOffset(c, w, filename, int64(allData.Len()))

	// read the data back from the file
	fullName := filepath.Join(relayDir, uuid, filename)
	obtainData, err := os.ReadFile(fullName)
	c.Assert(err, check.IsNil)
	c.Assert(obtainData, check.DeepEquals, allData.Bytes())
}

func (t *testFileWriterSuite) TestHandleFileHoleExist(c *check.C) {
	var (
		relayDir = c.MkDir()
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		filename = "test-mysql-bin.000001"
		header   = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
		}
		latestPos uint32 = 4
	)
	formatDescEv, err := event.GenFormatDescriptionEvent(header, latestPos)
	c.Assert(err, check.IsNil)
	c.Assert(formatDescEv, check.NotNil)

	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)

	w := NewFileWriter(log.L(), relayDir)
	defer w.Close()
	w.Init(uuid, filename)

	// write the FormatDescriptionEvent, no hole exists
	result, err := w.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)

	// hole exits, but the size is too small, invalid
	latestPos = formatDescEv.Header.LogPos + event.MinUserVarEventLen - 1
	queryEv, err := event.GenQueryEvent(header, latestPos, 0, 0, 0, nil, []byte("schema"), []byte("BEGIN"))
	c.Assert(err, check.IsNil)
	_, err = w.WriteEvent(queryEv)
	c.Assert(err, check.ErrorMatches, ".*generate dummy event.*")

	// hole exits, and the size is enough
	latestPos = formatDescEv.Header.LogPos + event.MinUserVarEventLen
	queryEv, err = event.GenQueryEvent(header, latestPos, 0, 0, 0, nil, []byte("schema"), []byte("BEGIN"))
	c.Assert(err, check.IsNil)
	result, err = w.WriteEvent(queryEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)
	fileSize := int64(queryEv.Header.LogPos)
	t.verifyFilenameOffset(c, w, filename, fileSize)

	// read events back from the file to check the dummy event
	events := make([]*replication.BinlogEvent, 0, 3)
	count := 0
	onEventFunc := func(e *replication.BinlogEvent) error {
		count++
		if count > 3 {
			c.Fatalf("too many events received, %+v", e.Header)
		}
		events = append(events, e)
		return nil
	}
	fullName := filepath.Join(relayDir, uuid, filename)
	err = replication.NewBinlogParser().ParseFile(fullName, 0, onEventFunc)
	c.Assert(err, check.IsNil)
	c.Assert(events, check.HasLen, 3)
	c.Assert(events[0], check.DeepEquals, formatDescEv)
	c.Assert(events[2], check.DeepEquals, queryEv)
	// the second event is the dummy event
	dummyEvent := events[1]
	c.Assert(dummyEvent.Header.EventType, check.Equals, replication.USER_VAR_EVENT)
	c.Assert(dummyEvent.Header.LogPos, check.Equals, latestPos)                               // start pos of the third event
	c.Assert(dummyEvent.Header.EventSize, check.Equals, latestPos-formatDescEv.Header.LogPos) // hole size
}

func (t *testFileWriterSuite) TestHandleDuplicateEventsExist(c *check.C) {
	// NOTE: not duplicate event already tested in other cases

	var (
		relayDir = c.MkDir()
		uuid     = "3ccc475b-2343-11e7-be21-6c0b84d59f30.000001"
		filename = "test-mysql-bin.000001"
		header   = &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			ServerID:  11,
		}
		latestPos uint32 = 4
	)
	c.Assert(os.MkdirAll(filepath.Join(relayDir, uuid), 0o755), check.IsNil)
	w := NewFileWriter(log.L(), relayDir)
	defer w.Close()
	w.Init(uuid, filename)

	// write a FormatDescriptionEvent, not duplicate
	formatDescEv, err := event.GenFormatDescriptionEvent(header, latestPos)
	c.Assert(err, check.IsNil)
	result, err := w.WriteEvent(formatDescEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)
	latestPos = formatDescEv.Header.LogPos

	// write a QueryEvent, the first time, not duplicate
	queryEv, err := event.GenQueryEvent(header, latestPos, 0, 0, 0, nil, []byte("schema"), []byte("BEGIN"))
	c.Assert(err, check.IsNil)
	result, err = w.WriteEvent(queryEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsFalse)

	// write the QueryEvent again, duplicate
	result, err = w.WriteEvent(queryEv)
	c.Assert(err, check.IsNil)
	c.Assert(result.Ignore, check.IsTrue)
	c.Assert(result.IgnoreReason, check.Equals, ignoreReasonAlreadyExists)

	// write a start/end pos mismatched event
	latestPos--
	queryEv, err = event.GenQueryEvent(header, latestPos, 0, 0, 0, nil, []byte("schema"), []byte("BEGIN"))
	c.Assert(err, check.IsNil)
	_, err = w.WriteEvent(queryEv)
	c.Assert(err, check.ErrorMatches, ".*handle a potential duplicate event.*")
}
