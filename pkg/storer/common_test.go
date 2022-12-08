package storer

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/digitalocean/apps-self-check/pkg/types/check"
	gomock "github.com/golang/mock/gomock"
)

func TestAsyncQueryRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	m := NewMockStorer(ctrl)
	c := &commonStorer{}
	c.init()

	m.EXPECT().SaveCheckResults(gomock.Any(), check.CheckResults{}).Return(errors.New("failed to save"))
	m.EXPECT().SaveCheckResults(gomock.Any(), check.CheckResults{
		Errors: []check.CheckError{
			{Check: "result_save_attempt_1", Error: "failed to save"},
		},
	}).Return(errors.New("failed to save on second attempt"))
	m.EXPECT().SaveCheckResults(gomock.Any(), check.CheckResults{
		Errors: []check.CheckError{
			{Check: "result_save_attempt_1", Error: "failed to save"},
			{Check: "result_save_attempt_2", Error: "failed to save on second attempt"},
		},
	}).Return(nil)

	r := check.CheckResults{}

	c.AsyncQueryRetry(ctx, []time.Duration{0, 1, 1}, func(ctx context.Context, attempt int) error {
		err := m.SaveCheckResults(ctx, r)
		if err != nil {
			r.Errors = append(r.Errors, check.CheckError{
				Check: "result_save_attempt_" + strconv.Itoa(attempt),
				Error: err.Error(),
			})
			return err
		}
		return nil
	})

	c.wg.Wait()
}
