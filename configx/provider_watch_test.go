package configx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ory/x/logrusx"
	"github.com/ory/x/watcherx"
)

func tmpConfigFile(t *testing.T, dsn, foo string) *os.File {
	config := fmt.Sprintf("dsn: %s\nfoo: %s\n", dsn, foo)

	tdir := os.TempDir() + "/" + strconv.Itoa(time.Now().Nanosecond())
	require.NoError(t,
		os.MkdirAll(tdir, // DO NOT CHANGE THIS: https://github.com/fsnotify/fsnotify/issues/340
			os.ModePerm))
	configFile, err := ioutil.TempFile(tdir, "config-*.yml")
	_, err = io.WriteString(configFile, config)
	require.NoError(t, err)
	require.NoError(t, configFile.Sync())
	t.Cleanup(func() {
		fmt.Printf("removing %s\n", configFile.Name())
		_ = os.Remove(configFile.Name())
	})

	return configFile
}

func updateConfigFile(t *testing.T, c <-chan struct{}, configFile *os.File, dsn, foo string) {
	config := fmt.Sprintf("dsn: %s\nfoo: %s\n", dsn, foo)

	_, err := configFile.Seek(0, 0)
	require.NoError(t, err)
	_, err = io.WriteString(configFile, config)
	require.NoError(t, configFile.Sync())
	<-c // Wait for changes to propagate
}

func TestReload(t *testing.T) {
	setup := func(t *testing.T, cf *os.File, c chan<- struct{}, modifiers ...OptionModifier) (*Provider, *logrusx.Logger) {
		l := logrusx.New("", "")
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		modifiers = append(modifiers,
			WithLogrusWatcher(l),
			AttachWatcher(func(event watcherx.Event, err error) {
				c <- struct{}{}
			}),
			WithContext(ctx),
		)
		p, err := newKoanf("./stub/watch/config.schema.json", []string{cf.Name()}, modifiers...)
		require.NoError(t, err)
		return p, l
	}

	t.Run("case=rejects not validating changes", func(t *testing.T) {
		configFile := tmpConfigFile(t, "memory", "bar")
		defer configFile.Close()
		c := make(chan struct{})
		p, l := setup(t, configFile, c)
		hook := test.NewLocal(l.Entry.Logger)

		assert.Equal(t, []*logrus.Entry{}, hook.AllEntries())
		assert.Equal(t, "memory", p.String("dsn"))
		assert.Equal(t, "bar", p.String("foo"))

		updateConfigFile(t, c, configFile, "memory", "not bar")

		entries := hook.AllEntries()
		require.Equal(t, 2, len(entries))

		assert.Equal(t, "A change to a configuration file was detected.", entries[0].Message)
		assert.Equal(t, "The changed configuration is invalid and could not be loaded. Rolling back to the last working configuration revision. Please address the validation errors before restarting the process.", entries[1].Message)

		assert.Equal(t, "memory", p.String("dsn"))
		assert.Equal(t, "bar", p.String("foo"))
	})

	t.Run("case=rejects to update immutable", func(t *testing.T) {
		configFile := tmpConfigFile(t, "memory", "bar")
		defer configFile.Close()
		c := make(chan struct{})
		p, l := setup(t, configFile, c,
			WithImmutables("dsn"))
		hook := test.NewLocal(l.Entry.Logger)

		assert.Equal(t, []*logrus.Entry{}, hook.AllEntries())
		assert.Equal(t, "memory", p.String("dsn"))
		assert.Equal(t, "bar", p.String("foo"))

		updateConfigFile(t, c, configFile, "some db", "bar")

		entries := hook.AllEntries()
		require.Equal(t, 2, len(entries))
		assert.Equal(t, "A change to a configuration file was detected.", entries[0].Message)
		assert.Equal(t, "A configuration value marked as immutable has changed. Rolling back to the last working configuration revision. To reload the values please restart the process.", entries[1].Message)
		assert.Equal(t, "memory", p.String("dsn"))
		assert.Equal(t, "bar", p.String("foo"))
	})

	t.Run("case=runs without validation errors", func(t *testing.T) {
		configFile := tmpConfigFile(t, "some string", "bar")
		defer configFile.Close()
		c := make(chan struct{})
		p, l := setup(t, configFile, c)
		hook := test.NewLocal(l.Entry.Logger)

		assert.Equal(t, []*logrus.Entry{}, hook.AllEntries())
		assert.Equal(t, "some string", p.String("dsn"))
		assert.Equal(t, "bar", p.String("foo"))
	})

	t.Run("case=has with validation errors", func(t *testing.T) {
		configFile := tmpConfigFile(t, "some string", "not bar")
		defer configFile.Close()
		l := logrusx.New("", "")
		hook := test.NewLocal(l.Entry.Logger)

		var b bytes.Buffer
		_, err := newKoanf("./stub/watch/config.schema.json", []string{configFile.Name()},
			WithStandardValidationReporter(&b),
			WithLogrusWatcher(l),
		)
		require.Error(t, err)

		entries := hook.AllEntries()
		require.Equal(t, 0, len(entries))
		assert.Equal(t, "The configuration contains values or keys which are invalid:\nfoo: not bar\n     ^-- value must be \"bar\"\n\n", b.String())
	})
}