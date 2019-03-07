package controller

import (
	"context"
	"sync"

	"github.com/Azure/Kore/pkg/scalers"

	kore_v1alpha1 "github.com/Azure/Kore/pkg/apis/kore/v1alpha1"
	clientset "github.com/Azure/Kore/pkg/client/clientset/versioned"
	koreinformer_v1alpha1 "github.com/Azure/Kore/pkg/client/informers/externalversions/kore/v1alpha1"
	log "github.com/Sirupsen/logrus"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type Controller interface {
	Run(ctx context.Context)
}

type controller struct {
	scaledObjectsInformer cache.SharedInformer
	ctx                   context.Context
	koreClient            clientset.Interface
	kubeClient            kubernetes.Interface
	scaleHandler          *scalers.ScaleHandler
	scaledObjectsContexts *sync.Map
}

func NewController(koreClient clientset.Interface, kubeClient kubernetes.Interface, scaleHandler *scalers.ScaleHandler) Controller {
	c := &controller{
		koreClient:   koreClient,
		kubeClient:   kubeClient,
		scaleHandler: scaleHandler,
		scaledObjectsInformer: koreinformer_v1alpha1.NewScaledObjectInformer(
			koreClient,
			meta_v1.NamespaceAll,
			0,
			cache.Indexers{},
		),
		scaledObjectsContexts: &sync.Map{},
	}

	c.scaledObjectsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: c.syncScaledObject,
		UpdateFunc: func(oldObj, newObj interface{}) {
			new := newObj.(*kore_v1alpha1.ScaledObject)
			old := oldObj.(*kore_v1alpha1.ScaledObject)
			if new.ResourceVersion == old.ResourceVersion {
				return
			}
			c.syncScaledObject(newObj)
		},
		DeleteFunc: c.syncDeletedScaledObject,
	})

	return c
}

func (c *controller) syncScaledObject(obj interface{}) {
	scaledObject := obj.(*kore_v1alpha1.ScaledObject)
	key, err := cache.MetaNamespaceKeyFunc(scaledObject)
	if err != nil {
		log.Errorf("Error getting key for scaledObject (%s/%s)", scaledObject.GetNamespace(), scaledObject.GetName())
		return
	}

	ctx, cancel := context.WithCancel(c.ctx)

	value, loaded := c.scaledObjectsContexts.LoadOrStore(key, cancel)
	if loaded {
		cancelValue, ok := value.(context.CancelFunc)
		if ok {
			cancelValue()
		}
		c.scaledObjectsContexts.Store(key, cancel)
	}
	c.scaleHandler.WatchScaledObjectWithContext(ctx, scaledObject)
}

func (c *controller) syncDeletedScaledObject(obj interface{}) {
	scaledObject := obj.(*kore_v1alpha1.ScaledObject)
	log.Debugf("Notified about deletion of ScaledObject: %s", scaledObject.GetName())

	key, err := cache.MetaNamespaceKeyFunc(scaledObject)
	if err != nil {
		log.Errorf("Error getting key for scaledObject (%s/%s)", scaledObject.GetNamespace(), scaledObject.GetName())
		return
	}

	result, ok := c.scaledObjectsContexts.Load(key)
	if ok {
		cancel, ok := result.(context.CancelFunc)
		if ok {
			cancel()
		}
		c.scaledObjectsContexts.Delete(key)
	} else {
		log.Debugf("ScaledObject %s not found in controller cache", key)
	}
}

func (c *controller) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.ctx = ctx
	go func() {
		<-ctx.Done()
		log.Infof("Controller is shutting down")
	}()
	log.Infof("Controller is started")
	c.scaledObjectsInformer.Run(ctx.Done())
	cancel()
}