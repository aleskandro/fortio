// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package periodic

import (
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fortio.org/fortio/log"
)

type Noop struct{}

func (n *Noop) Run(t int) {
}

// used for when we don't actually run periodic test/want to initialize
// watchers.
var bogusTestChan = NewAborter()

func TestNewPeriodicRunner(t *testing.T) {
	tests := []struct {
		qps                float64 // input
		numThreads         int     // input
		expectedQPS        float64 // expected
		expectedNumThreads int     // expected
	}{
		{qps: 0.1, numThreads: 1, expectedQPS: 0.1, expectedNumThreads: 1},
		{qps: 1, numThreads: 3, expectedQPS: 1, expectedNumThreads: 3},
		{qps: 10, numThreads: 10, expectedQPS: 10, expectedNumThreads: 10},
		{qps: 100000, numThreads: 10, expectedQPS: 100000, expectedNumThreads: 10},
		{qps: 0.5, numThreads: 1, expectedQPS: 0.5, expectedNumThreads: 1},
		// Error cases negative qps same as -1 qps == max speed
		{qps: -10, numThreads: 0, expectedQPS: -1, expectedNumThreads: 4},
		// Need at least 1 thread
		{qps: 0, numThreads: -6, expectedQPS: DefaultRunnerOptions.QPS, expectedNumThreads: 1},
	}
	for _, tst := range tests {
		o := RunnerOptions{
			QPS:        tst.qps,
			NumThreads: tst.numThreads,
			Stop:       bogusTestChan, // TODO: use bogusTestChan so gOutstandingRuns does reach 0
		}
		r := newPeriodicRunner(&o)
		r.MakeRunners(&Noop{})
		if r.QPS != tst.expectedQPS {
			t.Errorf("qps: got %f, not as expected %f", r.QPS, tst.expectedQPS)
		}
		if r.NumThreads != tst.expectedNumThreads {
			t.Errorf("threads: with %d input got %d, not as expected %d",
				tst.numThreads, r.NumThreads, tst.expectedNumThreads)
		}
		r.ReleaseRunners()
	}
}

type TestCount struct {
	count *int64
	lock  *sync.Mutex
}

func (c *TestCount) Run(i int) {
	c.lock.Lock()
	(*c.count)++
	c.lock.Unlock()
	time.Sleep(100 * time.Millisecond)
}

func TestStart(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        9.4,
		NumThreads: 1,
		Duration:   1 * time.Second,
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	r.Run()
	expected := int64(9)
	if count != expected {
		t.Errorf("Test executed unexpected number of times %d instead %d", count, expected)
	}
	count = 0
	oo := r.Options()
	oo.NumThreads = 10 // will be lowered to 4 so 8 calls (2 in each thread)
	r.Run()
	if count != 8 {
		t.Errorf("MT Test executed unexpected number of times %d instead %d", count, 8)
	}
	// note: it's kind of a bug this only works after Run() and not before
	if oo.NumThreads != 4 {
		t.Errorf("Lowering of thread count broken, got %d instead of 4", oo.NumThreads)
	}
	count = 0
	oo.Duration = 1 * time.Nanosecond
	r.Run()
	if count != 2 {
		t.Errorf("Test executed unexpected number of times %d instead minimum 2", count)
	}
	r.Options().ReleaseRunners()
}

func TestStartMaxQps(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        -1, // max speed (0 is default qps, not max)
		NumThreads: 4,
		Duration:   290 * time.Millisecond,
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	var res1 HasRunnerResult // test that interface
	res := r.Run()
	res1 = res.Result()
	expected := int64(3 * 4) // can start 3 100ms in 290ms * 4 threads
	// Check the count both from the histogram and from our own test counter:
	actual := res1.Result().DurationHistogram.Count
	if actual != expected {
		t.Errorf("MaxQpsTest executed unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("MaxQpsTest executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

func TestExactlyLargeDur(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        3,
		NumThreads: 4,
		Duration:   100 * time.Hour, // will not be used, large to catch if it would
		Exactly:    9,               // exactly 9 times, so 2 per thread + 1
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	expected := o.Exactly
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

func TestExactlySmallDur(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	expected := int64(11)
	o := RunnerOptions{
		QPS:        3,
		NumThreads: 4,
		Duration:   1 * time.Second, // would do only 3 calls without Exactly
		Exactly:    expected,        // exactly 11 times, so 2 per thread + 3
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

func TestExactlyMaxQps(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	expected := int64(503)
	o := RunnerOptions{
		QPS:        -1, // max qps
		NumThreads: 4,
		Duration:   -1,       // infinite but should not be used
		Exactly:    expected, // exactly 503 times, so 125 per thread + 3
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("Exact count executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

type testAccessLogger struct {
	sync.Mutex
	reports int64
}

func (t *testAccessLogger) Report(thread int, time int64, latency float64) {
	t.Lock()
	defer t.Unlock()
	t.reports++
}

func (t *testAccessLogger) Info() string {
	return "test access logger"
}

func TestAccessLogs(t *testing.T) {
	var count int64
	var lock sync.Mutex

	logger := &testAccessLogger{}
	c := TestCount{&count, &lock}
	expected := int64(10)
	o := RunnerOptions{
		QPS:          -1, // max qps
		NumThreads:   4,
		Duration:     -1, // infinite but should not be used
		Exactly:      expected,
		AccessLogger: logger,
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual != expected {
		t.Errorf("Access logs executed unexpected number of times %d instead %d", actual, expected)
	}
	if logger.reports != expected {
		t.Errorf("Access logs log unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("Access logs executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

func TestUniform(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	expected := int64(40)
	o := RunnerOptions{
		QPS:        100,
		NumThreads: 4,
		Duration:   time.Second,
		Uniform:    true,
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual != expected {
		t.Errorf("Uniform executed unexpected number of times %d instead %d", actual, expected)
	}
	if count != expected {
		t.Errorf("Uniform executed unexpected number of times %d instead %d", count, expected)
	}
	r.Options().ReleaseRunners()
}

func TestID(t *testing.T) {
	tests := []struct {
		labels string // input
		id     string // expected suffix after the date
	}{
		{"", ""},
		{"abcDEF123", "_abcDEF123"},
		{"A!@#$%^&*()-+=/'B", "_A_B"},
		// Ends with non alpha, skip last _
		{"A  ", "_A"},
		{" ", ""},
		// truncated to fit 96 (17 from date/time + _ + 78 from labels)
		{
			"123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890",
			"_123456789012345678901234567890123456789012345678901234567890123456789012345678",
		},
	}
	startTime := time.Date(2001, time.January, 2, 3, 4, 5, 0, time.Local)
	prefix := "2001-01-02-030405"
	for _, tst := range tests {
		o := RunnerResults{
			StartTime: startTime,
			Labels:    tst.labels,
		}
		id := o.ID()
		expected := prefix + tst.id
		if id != expected {
			t.Errorf("id: got %s, not as expected %s", id, expected)
		}
	}
}

func TestInfiniteDurationAndAbort(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        10,
		NumThreads: 1,
		Duration:   -1, // infinite but we'll abort after 1sec
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	go func() {
		time.Sleep(1 * time.Second)
		log.LogVf("Calling abort after 1 sec")
		r.Options().Abort()
	}()
	r.Run()
	if count < 9 || count > 13 {
		t.Errorf("Test executed unexpected number of times %d instead of 9-13", count)
	}
	// Same with infinite qps
	count = 0
	o.QPS = -1 // infinite qps
	r.Options().ReleaseRunners()
	r = NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	go func() {
		time.Sleep(140 * time.Millisecond)
		log.LogVf("Sending global interrupt after 0.14 sec")
		gAbortMutex.Lock()
		gAbortChan <- os.Interrupt
		gAbortMutex.Unlock()
	}()
	r.Run()
	r.Options().ReleaseRunners()
	if count < 2 || count > 4 { // should get 3 in 140ms
		t.Errorf("Test executed unexpected number of times %d instead of 3 (2-4)", count)
	}
}

func TestExactlyAndAbort(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        10,
		NumThreads: 1,
		Exactly:    100, // would take 10s we'll abort after 1sec
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	go func() {
		time.Sleep(1 * time.Second)
		log.LogVf("Calling abort after 1 sec")
		r.Options().Abort()
	}()
	res := r.Run()
	r.Options().ReleaseRunners()
	if count < 9 || count > 13 {
		t.Errorf("Test executed unexpected number of times %d instead of 9-13", count)
	}
	if !strings.Contains(res.RequestedDuration, "exactly 100 calls, interrupted after") {
		t.Errorf("Got '%s' and didn't find expected aborted", res.RequestedDuration)
	}
}

func TestSleepFallingBehind(t *testing.T) {
	var count int64
	var lock sync.Mutex
	c := TestCount{&count, &lock}
	o := RunnerOptions{
		QPS:        1000000, // similar to max qps but with sleep falling behind
		NumThreads: 4,
		Duration:   290 * time.Millisecond,
	}
	r := NewPeriodicRunner(&o)
	r.Options().MakeRunners(&c)
	count = 0
	res := r.Run()
	r.Options().ReleaseRunners()
	expected := int64(3 * 4) // can start 3 100ms in 290ms * 4 threads
	// Check the count both from the histogram and from our own test counter:
	actual := res.DurationHistogram.Count
	if actual > expected+2 || actual < expected-2 {
		t.Errorf("Extra high qps executed unexpected number of times %d instead %d", actual, expected)
	}
	// check histogram and our counter got same result
	if count != actual {
		t.Errorf("Extra high qps internal counter %d doesn't match histogram %d for expected %d", count, actual, expected)
	}
}

func Test2Watchers(t *testing.T) {
	// Wait for previous test to cleanup watchers
	time.Sleep(200 * time.Millisecond)
	o1 := RunnerOptions{}
	r1 := newPeriodicRunner(&o1)
	o2 := RunnerOptions{}
	r2 := newPeriodicRunner(&o2)
	time.Sleep(200 * time.Millisecond)
	gAbortMutex.Lock()
	if gOutstandingRuns != 2 {
		t.Errorf("found %d watches while expecting 2 for (%v %v)", gOutstandingRuns, r1, r2)
	}
	gAbortMutex.Unlock()
	gAbortChan <- os.Interrupt
	// wait for interrupt to propagate
	time.Sleep(200 * time.Millisecond)
	gAbortMutex.Lock()
	if gOutstandingRuns != 0 {
		t.Errorf("found %d watches while expecting 0", gOutstandingRuns)
	}
	gAbortMutex.Unlock()
}

func TestGetJitter(t *testing.T) {
	d := getJitter(4)
	if d != time.Duration(0) {
		t.Errorf("getJitter < 5 got %v instead of expected 0", d)
	}
	sum := 0.
	for i := 0; i < 100; i++ {
		d = getJitter(6)
		a := math.Abs(float64(d))
		// only valid values are -1, 0, 1
		if a != 1. && d != 0 {
			t.Errorf("getJitter 6 got %v (%v) instead of expected -1/+1", d, a)
		}
		// make sure we don't always get 0
		sum += a
	}
	if sum <= 60 {
		t.Errorf("getJitter 6 got %v sum of abs value instead of expected > 60 at -1/+1", sum)
	}
}
