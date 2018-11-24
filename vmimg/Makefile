.PHONY: all
all:
	docker build -t virtuakube-fs .
	docker run --mount=type=bind,source=$$(pwd),destination=/tmp/ctx virtuakube-fs cp /vmlinuz /initrd.img /tmp/ctx
	docker export $$(docker run -d virtuakube-fs /bin/true) >virtuakube.tar
	virt-make-fs --partition --format=qcow2 --type=ext4 --size=10G virtuakube.tar virtuakube-large.qcow2
	rm -f virtuakube.tar
	qemu-system-x86_64 \
		-enable-kvm \
		-m 1024 \
		-device virtio-net,netdev=net0 \
		-device virtio-rng-pci,rng=rng0 \
		-object rng-random,filename=/dev/urandom,id=rng0 \
		-netdev user,id=net0 \
		-drive if=virtio,file=virtuakube-large.qcow2,media=disk \
		-virtfs local,path=$$(pwd),mount_tag=host0,security_model=none,id=host0 \
		-kernel ./vmlinuz -initrd ./initrd.img \
		-append "root=/dev/vda1 systemd.journald.forward_to_console"
	qemu-img convert -O qcow2 -c virtuakube-large.qcow2 virtuakube.qcow2
	rm -f virtuakube-large.qcow2 vmlinuz initrd.img
