package boltstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	//"time"

	bolt "go.etcd.io/bbolt"
	"github.com/balaji-balu/ieo/pkg/model"
)

// -------------------- Internal write request --------------------

type writeRequest struct {
	fn   func(tx *bolt.Tx) error
	resp chan error
}

// -------------------- Store --------------------

type StateStore struct {
	db         *bolt.DB
	writeQueue chan writeRequest
	stopChan   chan struct{}
	closeOnce  sync.Once
}

func (s *StateStore) Close() error {
	s.closeOnce.Do(func() {
		close(s.stopChan)
	})
	return s.db.Close()
}

// -------------------- Opening / Closing --------------------

func NewStateStore(path string) (*StateStore, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 0})
	if err != nil {
		return nil, err
	}

	s := &StateStore{
		db:         db,
		writeQueue: make(chan writeRequest, 1024),
		stopChan:   make(chan struct{}),
	}

	// Start the single writer goroutine
	go s.writerLoop()

	return s, nil
}

// -------------------- Writer Loop --------------------

func (s *StateStore) writerLoop() {
	for {
		select {
		case req := <-s.writeQueue:
			err := s.db.Update(func(tx *bolt.Tx) error {
				return req.fn(tx)
			})
			req.resp <- err

		case <-s.stopChan:
			return
		}
	}
}

// Public write entry point
func (s *StateStore) write(fn func(tx *bolt.Tx) error) error {
	resp := make(chan error, 1)
	s.writeQueue <- writeRequest{fn: fn, resp: resp}
	return <-resp
}

// -------------------- Bucket Helpers --------------------

func (s *StateStore) GetOrCreateBucket(tx *bolt.Tx, path []string) (*bolt.Bucket, error) {
	if len(path) == 0 {
		return nil, errors.New("empty bucket path")
	}

	b := tx.Bucket([]byte(path[0]))
	var err error

	if b == nil {
		b, err = tx.CreateBucket([]byte(path[0]))
		if err != nil {
			return nil, err
		}
	}

	for _, name := range path[1:] {
		nb := b.Bucket([]byte(name))
		if nb == nil {
			nb, err = b.CreateBucket([]byte(name))
			if err != nil {
				return nil, err
			}
		}
		b = nb
	}

	return b, nil
}

func (s *StateStore) GetBucket(tx *bolt.Tx, path []string) *bolt.Bucket {
	if len(path) == 0 {
		return nil
	}

	b := tx.Bucket([]byte(path[0]))
	if b == nil {
		return nil
	}

	for _, name := range path[1:] {
		b = b.Bucket([]byte(name))
		if b == nil {
			return nil
		}
	}
	return b
}

// -------------------- JSON Helpers --------------------

func (s *StateStore) SaveJSON(b *bolt.Bucket, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return b.Put([]byte(key), data)
}

func (s *StateStore) LoadJSON(b *bolt.Bucket, key string, v any) error {
	raw := b.Get([]byte(key))
	if raw == nil {
		return fmt.Errorf("key '%s' not found", key)
	}
	return json.Unmarshal(raw, v)
}

// -------------------- High-level API --------------------

func (s *StateStore) SaveState(path []string, key string, v any) error {
	return s.write(func(tx *bolt.Tx) error {
		b, err := s.GetOrCreateBucket(tx, path)
		if err != nil {
			return err
		}
		return s.SaveJSON(b, key, v)
	})
}

func (s *StateStore) LoadState(path []string, key string, v any) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := s.GetBucket(tx, path)
		if b == nil {
			return fmt.Errorf("bucket path %v not found", path)
		}
		return s.LoadJSON(b, key, v)
	})
}

func (s *StateStore) LoadActualForHost(host string) (map[string]model.ActualApp, error) {
	result := make(map[string]model.ActualApp)

	err := s.db.View(func(tx *bolt.Tx) error {
		hostBkt := s.GetBucket(tx, []string{"actual", host})
		if hostBkt == nil {
			return fmt.Errorf("host %s not found", host)
		}

		return hostBkt.ForEach(func(k, v []byte) error {
			// If v==nil => this is a nested bucket, skip (should not happen for apps)
			if v == nil {
				return nil
			}

			var app model.ActualApp
			if err := json.Unmarshal(v, &app); err != nil {
				return err
			}
			result[string(k)] = app
			return nil
		})
	})

	return result, err
}


func (s *StateStore) LoadAllHosts() (map[string]model.Host, error) {
	hosts := make(map[string]model.Host)

	err := s.db.View(func(tx *bolt.Tx) error {
		b := s.GetBucket(tx, []string{"hosts"})
		if b == nil {
			return fmt.Errorf("hosts bucket missing")
		}

		return b.ForEach(func(k, v []byte) error {
			var info model.Host
			if err := json.Unmarshal(v, &info); err != nil {
				return err
			}
			hosts[string(k)] = info
			return nil
		})
	})

	return hosts, err
}

func (s *StateStore) AddOrUpdateHost(host model.Host) (error) {
	path := []string{"hosts"}
	key:= host.ID
	if err := s.SaveState(path, key, host); err != nil {
		return fmt.Errorf("failed to save desired state for %s/%s: %v", path, key, err)
	}
	return nil
}


func (s *StateStore) SetDesired(depId string, app model.App) (error) {
	log.Println("SetDesired depid:", depId, app)
	path := []string{"desired", depId}
	key := "app" // could also be "deploy-" + appID or version
	if err := s.SaveState(path, key, app); err != nil {
		return fmt.Errorf("failed to save desired state for %s/%s: %v", path, key, err)
	}
	return nil
}

func (s *StateStore) GetDesired(depId string) (model.App, error) {
	log.Println("depid:", depId)
	desired := model.App{}
	path := []string{"desired", depId}
	key := "app" // could also be "deploy-" + appID or version
	if err := s.LoadState(path, key, &desired); err != nil {
		return model.App{}, fmt.Errorf("failed to save desired state for %s/%s: %v", path, key, err)
	}
	log.Println("desired:", desired)
	return desired, nil
}

func (s *StateStore) GetActual() (model.ActualState, error) {
	actual := model.ActualState{
		AppsByHost: map[string]map[string]model.ActualApp{},
	}

	hosts, _ := s.LoadAllHosts()
	for hostid, _ := range hosts {
		a, err := s.LoadActualForHost(hostid)
		if err != nil {
            // log and continue if host state not found; or return - choose one
            //log.Printf("load actual for host %s: %v (continuing)", hostid, err)
            a = map[string]model.ActualApp{}
        }
		actual.AppsByHost[hostid] = a
	}

	return actual, nil
}

func (s *StateStore) SetActual(hostid string, app model.ActualApp) (error) {
	path := []string{"actual", hostid}
	s.SaveState(path, app.ID, &app )
	return nil
}

func (s *StateStore) SetOperation(depId string, op model.DiffOp){
	path := []string{"operations"}
	key := fmt.Sprintf("%s-%d", depId, op.TimeStamp)
	s.SaveState(path, key, op)
}

func (s *StateStore) GetOperation(depId string, timestamp int64) (model.DiffOp, error) {
    path := []string{"operations"}
    key := fmt.Sprintf("%s-%d", depId, timestamp)

    var op model.DiffOp
    err := s.LoadState(path, key, &op)
    if err != nil {
        return model.DiffOp{}, err
    }

    return op, nil
}
