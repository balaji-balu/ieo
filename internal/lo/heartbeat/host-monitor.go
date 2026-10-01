package heartbeat

import (
    "log"
    "sync"
    "time"

    "github.com/balaji-balu/ieo/internal/lo/boltstore"
)

type Status int

const (
    Alive Status = iota
    Dead
)

type NodeState struct {
    LastSeen time.Time
    Misses   int
    Status   Status
}

type Monitor struct {
    mu            sync.Mutex
    state         map[string]*NodeState
    ExpectedEvery time.Duration
    MaxMisses     int

    OnDead     func(enID string)
    OnRecovery func(enID string)

    //store *reconciler.BoltStore
    store *boltstore.StateStore
}

func NewMonitor(expectedEvery time.Duration, 
    maxMisses int, store *boltstore.StateStore) *Monitor {
        
    return &Monitor{
        state:         make(map[string]*NodeState),
        ExpectedEvery: expectedEvery,
        MaxMisses:     maxMisses,
        store:         store,
    }
}

func pathForHeartbeat(status string) []string {
	return []string{"heartbeat", status} 
}

func (m *Monitor) Update(enID string) {
    m.mu.Lock()
    defer m.mu.Unlock()

    now := time.Now()
    s, ok := m.state[enID]

    if !ok {
        // First ever heartbeat
        m.state[enID] = &NodeState{
            LastSeen: now,
            Status:   Alive,
            Misses:   0,
        }
        //_ = m.store.SetHostAlive(enID, now) // stores alive + misses=0
        _ = m.store.SaveState(pathForHeartbeat("alive"), enID, now)
        log.Printf("[INFO] EN %s ALIVE (new)", enID)
        return
    }

    if s.Status == Dead {
        // Recovery
        s.Status = Alive
        s.LastSeen = now
        s.Misses = 0

        //_ = m.store.SetHostAlive(enID, now) // updates status=alive + misses=0
        _ = m.store.SaveState(pathForHeartbeat("alive"), enID, now)
        log.Printf("[INFO] EN %s RECOVERED", enID)

        if m.OnRecovery != nil {
            go m.OnRecovery(enID)
        }
        return
    }

    // Normal heartbeat
    s.LastSeen = now
    s.Misses = 0

    //_ = m.store.SetHostAlive(enID, now) // always resets misses=0
    _ = m.store.SaveState(pathForHeartbeat("alive"), enID, now)
}


func (m *Monitor) Start() {
    go func() {
        ticker := time.NewTicker(m.ExpectedEvery)
        defer ticker.Stop()

        for range ticker.C {
            m.check()
        }
    }()
}

func (m *Monitor) check() {
    m.mu.Lock()
    defer m.mu.Unlock()

    now := time.Now()

    for enID, s := range m.state {
        if s.Status == Dead {
            continue
        }

        if now.Sub(s.LastSeen) > m.ExpectedEvery {
            s.Misses++

            //_ = m.store.IncrementMisses(enID, s.Misses)
            _ = m.store.SaveState(pathForHeartbeat("misses"), enID, now)
            log.Printf("[WARN] EN %s missed %d/%d", enID, s.Misses, m.MaxMisses)

            if s.Misses >= m.MaxMisses {
                s.Status = Dead

                //_ = m.store.SetHostDead(enID, s.LastSeen)
                _ = m.store.SaveState(pathForHeartbeat("notactive"), enID, now)

                log.Printf("[ERROR] EN %s declared DEAD", enID)

                if m.OnDead != nil {
                    go m.OnDead(enID)
                }
            }
        }
    }
}
