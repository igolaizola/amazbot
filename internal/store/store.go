package store

import (
	"encoding/json"
	"fmt"

	"github.com/boltdb/bolt"
)

func New(path string) (*Store, error) {
	// Open the my.db data file in your current directory.
	// It will be created if it doesn't exist.
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("store: couldn't open bold db %s: %w", path, err)
	}
	for _, bucket := range []string{"db", "config"} {
		if err := db.Update(func(tx *bolt.Tx) error {
			if _, err := tx.CreateBucketIfNotExists([]byte(bucket)); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("store: couldn't create bucket %s: %w", bucket, err)
		}
	}
	return &Store{db: db}, nil
}

type Store struct {
	db *bolt.DB
}

func (s *Store) Close() {
	s.db.Close()
}

func (s *Store) Keys(bucket string) ([]string, error) {
	var keys []string
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		return b.ForEach(func(k, v []byte) error {
			keys = append(keys, string(k))
			return nil
		})
	}); err != nil {
		return nil, fmt.Errorf("store: couldn't get keys: %w", err)
	}
	return keys, nil
}

func (s *Store) Get(bucket, key string, val interface{}) error {
	if err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if v := b.Get([]byte(key)); len(v) > 0 {
			if err := json.Unmarshal(v, val); err != nil {
				return fmt.Errorf("couldn't decode: %w", err)
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("store: couldn't get %s: %w", key, err)
	}
	return nil
}

func (s *Store) Put(bucket, key string, val interface{}) error {
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		byt, err := json.Marshal(val)
		if err != nil {
			return fmt.Errorf("couldn't encode: %w", err)
		}
		return b.Put([]byte(key), byt)
	}); err != nil {
		return fmt.Errorf("store: couldn't put %s: %w", key, err)
	}
	return nil
}

func (s *Store) Delete(bucket, key string) error {
	if err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		return b.Delete([]byte(key))
	}); err != nil {
		return fmt.Errorf("store: couldn't delete %s: %w", key, err)
	}
	return nil
}
