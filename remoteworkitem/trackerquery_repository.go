package remoteworkitem

import (
	"fmt"
	"log"
	"strconv"

	"github.com/almighty/almighty-core/app"
	"github.com/almighty/almighty-core/models"
	"golang.org/x/net/context"
)

// GormTrackerQueryRepository implements TrackerRepository using gorm
type GormTrackerQueryRepository struct {
	ts *models.GormTransactionSupport
}

// NewTrackerQueryRepository constructs a TrackerQueryRepository
func NewTrackerQueryRepository(ts *models.GormTransactionSupport) *GormTrackerQueryRepository {
	return &GormTrackerQueryRepository{ts}
}

// Create creates a new tracker query in the repository
// returns BadParameterError, ConversionError or InternalError
func (r *GormTrackerQueryRepository) Create(ctx context.Context, query string, schedule string, tracker string) (*app.TrackerQuery, error) {
	tid, err := strconv.ParseUint(tracker, 10, 64)
	if err != nil {
		// treating this as a not found error: the fact that we're using number internal is implementation detail
		return nil, NotFoundError{"tracker", tracker}
	}
	fmt.Printf("tracker id: %v", tid)
	tq := TrackerQuery{
		Query:     query,
		Schedule:  schedule,
		TrackerID: tid}
	tx := r.ts.TX()
	if err := tx.Create(&tq).Error; err != nil {
		return nil, InternalError{simpleError{err.Error()}}
	}
	log.Printf("created tracker query %v\n", tq)
	tq2 := app.TrackerQuery{
		ID:        strconv.FormatUint(tq.ID, 10),
		Query:     query,
		Schedule:  schedule,
		TrackerID: tracker}

	return &tq2, nil
}

// Load returns the tracker query for the given id
// returns NotFoundError, ConversionError or InternalError
func (r *GormTrackerQueryRepository) Load(ctx context.Context, ID string) (*app.TrackerQuery, error) {
	id, err := strconv.ParseUint(ID, 10, 64)
	if err != nil {
		// treating this as a not found error: the fact that we're using number internal is implementation detail
		return nil, NotFoundError{"tracker query", ID}
	}

	log.Printf("loading tracker query %d", id)
	res := TrackerQuery{}
	if r.ts.TX().First(&res, id).RecordNotFound() {
		log.Printf("not found, res=%v", res)
		return nil, NotFoundError{"tracker query", ID}
	}
	tq := app.TrackerQuery{
		ID:        strconv.FormatUint(res.ID, 10),
		Query:     res.Query,
		Schedule:  res.Schedule,
		TrackerID: strconv.FormatUint(res.TrackerID, 10)}

	return &tq, nil
}

// Save updates the given tracker query in storage.
// returns NotFoundError, ConversionError or InternalError
func (r *GormTrackerQueryRepository) Save(ctx context.Context, tq app.TrackerQuery) (*app.TrackerQuery, error) {
	res := TrackerQuery{}
	id, err := strconv.ParseUint(tq.ID, 10, 64)
	if err != nil {
		return nil, NotFoundError{entity: "trackerquery", ID: tq.ID}
	}

	tid, err := strconv.ParseUint(tq.TrackerID, 10, 64)
	if err != nil {
		// treating this as a not found error: the fact that we're using number internal is implementation detail
		return nil, NotFoundError{"tracker", tq.TrackerID}
	}

	log.Printf("looking for id %d", id)
	tx := r.ts.TX()
	if tx.First(&res, id).RecordNotFound() {
		log.Printf("not found, res=%v", res)
		return nil, NotFoundError{entity: "TrackerQuery", ID: tq.ID}
	}

	if tx.First(&Tracker{}, tid).RecordNotFound() {
		log.Printf("not found, id=%d", id)
		return nil, NotFoundError{entity: "tracker", ID: tq.TrackerID}
	}

	newTq := TrackerQuery{
		ID:        id,
		Schedule:  tq.Schedule,
		Query:     tq.Query,
		TrackerID: tid}

	if err := tx.Save(&newTq).Error; err != nil {
		log.Print(err.Error())
		return nil, InternalError{simpleError{err.Error()}}
	}
	log.Printf("updated tracker query to %v\n", newTq)
	t2 := app.TrackerQuery{
		ID:        tq.ID,
		Schedule:  tq.Schedule,
		Query:     tq.Query,
		TrackerID: tq.TrackerID}

	return &t2, nil
}