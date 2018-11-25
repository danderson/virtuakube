.PHONY: update-addons
update-addons:
# Calico
	curl https://docs.projectcalico.org/v3.3/getting-started/kubernetes/installation/hosted/rbac-kdd.yaml >internal/assets/net/calico.yaml
	echo "---" >>internal/assets/net/calico.yaml
	curl https://docs.projectcalico.org/v3.3/getting-started/kubernetes/installation/hosted/kubernetes-datastore/calico-networking/1.7/calico.yaml >>internal/assets/net/calico.yaml
# Weave
	curl -L 'https://cloud.weave.works/k8s/net?k8s-version=1.12.2' >internal/assets/net/weave.yaml

	(cd internal/assets && go generate)
	grep -h "image:" internal/assets/*.yaml internal/assets/net/*.yaml | cut -f2- -d: | tr -d "'\" " >vmimg/addon-images
