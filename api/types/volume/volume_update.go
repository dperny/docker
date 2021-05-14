package volume // import "github.com/docker/docker/api/types/volume"

// VolumeUpdateBody is configuration to update a Volume with.
type VolumeUpdateBody struct {
	// Spec is the ClusterVolumeSpec to update the volume to.
	Spec *ClusterVolumeSpec `json:"Spec,omitempty"`
}
