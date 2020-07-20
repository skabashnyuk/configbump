package configmaps

import (
	"context"
	"crypto/md5"
	"io/ioutil"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var log = logf.Log.WithName("configmaps")

// ConfigMapReconcilerConfig is the configuration of the reconciler
type ConfigMapReconcilerConfig struct {
	BaseDir         string
	Labels          string
	Namespace       string
	OnReconcileDone func() error
}

type configMapReconciler struct {
	client       client.Client
	clientConfig *rest.Config
	config       ConfigMapReconcilerConfig
	selector     labels.Selector
	baseDir      string
	namespace    string
}

// configFiles is a map where keys are the names of the files and values are digests of their content
type configFiles = map[string][16]byte

// New creates a config map reconciler with given configuration and configures a controller for it
func New(mgr manager.Manager, config ConfigMapReconcilerConfig) error {
	lbls, err := labels.ConvertSelectorToLabelsMap(config.Labels)
	if err != nil {
		return err
	}

	r := &configMapReconciler{
		client:       mgr.GetClient(),
		clientConfig: mgr.GetConfig(),
		config:       config,
		selector:     lbls.AsSelector(),
	}

	// register the controller with the manager
	bld := builder.ControllerManagedBy(mgr)
	bld.Named("config-bump")
	bld.ForType(&corev1.ConfigMap{})
	// note that we do NOT set up the filter to only included the labeled config maps.
	// That way we would never see the events about deleted config maps or config maps from which
	// the label has been removed
	//bld.WithEventFilter(predicate.ResourceFilterPredicate{Selector: r.selector})
	if err = bld.Complete(r); err != nil {
		return err
	}

	r.sync(false)

	return nil
}

// sync performs the sync of the local set of files with the configured config maps
func (c *configMapReconciler) sync(managerRunning bool) error {
	var cl client.Client
	if managerRunning {
		cl = c.client
	} else {
		x, err := client.New(c.clientConfig, client.Options{})
		if err != nil {
			return err
		}
		cl = x
	}

	list := &corev1.ConfigMapList{}
	opts := []client.ListOption{
		client.InNamespace(c.config.Namespace),
		client.MatchingLabelsSelector{Selector: c.selector},
	}

	if err := cl.List(context.TODO(), list, opts...); err != nil {
		return err
	}

	processedFiles := make([]string, 0, 8)

	for _, cm := range list.Items {
		for name, data := range cm.Data {
			path := filepath.Join(c.config.BaseDir, name)
			doWrite := false
			if _, err := os.Stat(path); err == nil || os.IsExist(err) {
				// if the file exists
				if content, err := ioutil.ReadFile(path); err != nil {
					log.Error(err, "Failed to open the config file to see if it changed", "file", path)
				} else {
					dataHash := md5.Sum([]byte(data))
					contentHash := md5.Sum([]byte(content))

					doWrite = dataHash != contentHash
				}
			} else {
				// the file doesn't exist
				doWrite = true
			}

			if doWrite {
				if f, err := os.Create(path); err != nil {
					log.Error(err, "Failed to create a file for the configmap",
						"file", path, "namespace", cm.GetObjectMeta().GetNamespace(), "name", cm.GetObjectMeta().GetName())
				} else {
					defer f.Close()
					f.Write([]byte(data))
				}
			}

			processedFiles = append(processedFiles, path)
		}
	}

	// now go through all the existing files and delete those we have not processed while reading the config maps
	files, err := ioutil.ReadDir(c.config.BaseDir)
	if err != nil {
		return err
	}

	for _, f := range files {
		if !f.IsDir() {
			path := filepath.Join(c.config.BaseDir, f.Name())
			found := false
			for _, v := range processedFiles {
				if v == path {
					found = true
					break
				}
			}

			if found {
				continue
			}

			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}

	return nil
}

// Reconcile handles the changes in the configured config maps
func (c *configMapReconciler) Reconcile(r reconcile.Request) (reconcile.Result, error) {
	err := c.sync(true)
	return reconcile.Result{}, err
}
