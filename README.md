# k8s-event-driven-autoscaling

Autoscaling on Kubernetes that reacts to real demand, proven with numbers in CI:
a Go API scaled by KEDA on CPU and on Prometheus requests/s per pod, and a Go
queue worker scaled on Redis Streams consumer-group lag, including scale to zero.

Full documentation and the measured results are being added.
