"""Image orchestration rejects failed smoke checks and preserves profile differences."""

import importlib.util
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock

import json
import yaml


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "image_smoke.py"
SPEC = importlib.util.spec_from_file_location("image_smoke", SCRIPT)
IMAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(IMAGE)
SHA = "a" * 40


class Runner:
    def __init__(self):
        self.calls = []
        self.public_status = "404"
        self.metrics = "go_goroutines 1"
        self.exit_status = "0"
        self.port = "127.0.0.1:19091"

    def __call__(self, arguments, **options):
        self.calls.append((arguments, options))
        if arguments[-1] == "help":
            return "usage: service serve"
        if arguments[-1] == "-h":
            return "loopback listen address"
        if arguments[:3] == ["docker", "run", "--rm"] and arguments[-1] == "serve":
            return "USER_GRPC_ADDR is required"
        if arguments[:2] == ["docker", "port"]:
            return "127.0.0.1:18080" if arguments[-1] == "8080/tcp" else self.port
        if arguments[:2] == ["docker", "logs"]:
            return "version=" + SHA
        if arguments[:2] == ["docker", "inspect"]:
            return self.exit_status
        if arguments[0] == "curl":
            if "%{http_code}" in arguments:
                return self.public_status
            if arguments[-1].endswith("/metrics"):
                return self.metrics
            return "ok"
        return ""


class ImageSmokeTests(unittest.TestCase):
    def test_primary_profiles_build_tested_tags_with_source_revision(self):
        for service in sorted(IMAGE.SERVICES):
            with self.subTest(service=service):
                run = Runner()
                image = IMAGE.build_images(service, False, SHA, environ={}, run=run)
                self.assertEqual(image, ("service" if service == "user" else service) + ":ci")
                build = run.calls[0]
                self.assertEqual(build[0], ["docker", "build", "--build-arg", "VERSION=" + SHA,
                                             "-t", image, "."])
                self.assertEqual(build[1]["timeout"], 600)
                self.assertIn((["docker", "run", "--rm", image, "help"], {"expected": 2}), run.calls)

    def test_bad_profile_fake_or_revision_never_starts_docker(self):
        cases = [("service; echo x", False, SHA), ("assistant", True, "main"),
                 ("user", True, SHA), ("assistant", "true", SHA)]
        for service, fake, sha in cases:
            with self.subTest(service=service, fake=fake, sha=sha):
                run = Runner()
                with self.assertRaises(ValueError):
                    IMAGE.build_images(service, fake, sha, environ={}, run=run)
                self.assertFalse(run.calls)

    def test_optional_token_is_a_private_file_and_always_removed(self):
        with tempfile.TemporaryDirectory() as directory:
            for failure in [False, True]:
                paths = []

                def run(arguments, **options):
                    if arguments[:2] == ["docker", "build"]:
                        value = arguments[arguments.index("--secret") + 1]
                        path = Path(value.removeprefix("id=netrc,src="))
                        paths.append(path)
                        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                        self.assertEqual(path.read_text(), "machine github.com login x-access-token password sample-token\n")
                        self.assertNotIn("sample-token", str(arguments))
                        if failure:
                            raise RuntimeError("build failed")
                    return "usage:"

                environment = {"GO_MODULES_TOKEN": "sample-token", "RUNNER_TEMP": directory}
                if failure:
                    with self.assertRaisesRegex(RuntimeError, "build failed"):
                        IMAGE.build_images("service", False, SHA, environ=environment, run=run)
                else:
                    IMAGE.build_images("service", False, SHA, environ=environment, run=run)
                self.assertEqual(len(paths), 1)
                self.assertFalse(paths[0].exists())

    def test_missing_help_output_fails_before_artifact_can_be_created(self):
        def run(arguments, **options):
            return "unrelated output"

        with self.assertRaisesRegex(RuntimeError, "usage:"):
            IMAGE.build_images("service", False, SHA, environ={}, run=run)

    def test_assistant_builds_and_smokes_fake_provider_only_when_requested(self):
        for enabled in [False, True]:
            with self.subTest(fake_llm=enabled):
                run = Runner()
                IMAGE.build_images("assistant", enabled, SHA, environ={}, run=run)
                fake_builds = [args for args, _ in run.calls if "Dockerfile.fake-llm" in args]
                self.assertEqual(len(fake_builds), int(enabled))
                if enabled:
                    self.assertIn((["docker", "run", "--rm", "fake-llm:ci", "-h"], {}), run.calls)

    def test_bff_production_refusal_and_listener_shutdown_checks(self):
        run = Runner()
        IMAGE.build_images("bank-bff", False, SHA, environ={}, run=run)
        self.assertIn((["docker", "run", "--rm", "bank-bff:ci", "serve"], {"expected": 1}), run.calls)
        public = [args[-1] for args, _ in run.calls if args[0] == "curl" and "%{http_code}" in args]
        self.assertEqual(public, ["http://127.0.0.1:18080" + path
                                  for path in ["/metrics", "/healthz", "/readyz", "/v1/profile"]])
        readiness = [(args, options) for args, options in run.calls
                     if args[0] == "curl" and args[-1].endswith("/readyz") and "--retry" in args]
        self.assertEqual(len(readiness), 1)
        self.assertEqual(readiness[0][1]["timeout"], 90)
        self.assertEqual(run.calls[-1][0][:3], ["docker", "rm", "-f"])

    def test_bff_failure_always_removes_started_container(self):
        for property_name, value in [("public_status", "200"), ("metrics", "unrelated"),
                                      ("exit_status", "1"), ("port", "example.com:80")]:
            with self.subTest(failure=property_name):
                run = Runner()
                setattr(run, property_name, value)
                with self.assertRaises(RuntimeError):
                    IMAGE.build_images("bank-bff", False, SHA, environ={}, run=run)
                self.assertEqual(run.calls[-1][0][:3], ["docker", "rm", "-f"])

    def test_bff_rejects_wrong_prod_failure_reason_before_starting_dev(self):
        run = Runner()

        def wrong_reason(arguments, **options):
            if arguments[-1] == "serve":
                return "some other failure"
            return run(arguments, **options)

        with self.assertRaisesRegex(RuntimeError, "USER_GRPC_ADDR"):
            IMAGE.build_images("bank-bff", False, SHA, environ={}, run=wrong_reason)
        self.assertFalse(any("--name" in args for args, _ in run.calls))

    def test_command_passes_deadline_and_removes_token_from_child_environment(self):
        with mock.patch.dict(os.environ, {"GO_MODULES_TOKEN": "sample-token"}):
            with mock.patch.object(IMAGE.subprocess, "run", return_value=subprocess.CompletedProcess([], 2, "usage:")) as execute:
                self.assertEqual(IMAGE.command(["docker", "run"], expected=2, timeout=7), "usage:")
                self.assertEqual(execute.call_args.kwargs["timeout"], 7)
                self.assertNotIn("GO_MODULES_TOKEN", execute.call_args.kwargs["env"])
            with mock.patch.object(IMAGE.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "usage:")):
                with self.assertRaisesRegex(RuntimeError, "expected 2"):
                    IMAGE.command(["docker", "run"], expected=2)


class ImageWorkflowTests(unittest.TestCase):
    def workflow(self):
        path = SCRIPT.parents[2] / ".github/workflows/shared-image.yml"
        return yaml.safe_load(path.read_text())

    def run_step(self, name, *, revision=SHA, service="service", fake="false", local_revision=SHA):
        step = next(item for item in self.workflow()["jobs"]["image"]["steps"]
                    if item.get("name") == name)
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / ".finassist-ci.json").write_text(json.dumps({"revision": local_revision}))
            return subprocess.run(["bash", "-euo", "pipefail", "-c", step["run"]],
                                  cwd=directory, env={**os.environ, "CI_REVISION": revision,
                                                      "SERVICE": service, "FAKE_LLM": fake},
                                  capture_output=True, text=True, timeout=5)

    def test_workflow_never_passes_repository_secret_to_pull_request_code(self):
        workflow = self.workflow()
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        self.assertGreater(workflow["jobs"]["image"]["timeout-minutes"], 0)
        steps = workflow["jobs"]["image"]["steps"]
        for step in steps:
            self.assertNotIn("${{", step.get("run", ""))
            if step.get("uses", "").startswith("actions/checkout@"):
                self.assertIs(step["with"]["persist-credentials"], False)
            if "GO_MODULES_TOKEN" in step.get("env", {}):
                self.assertEqual(step["env"]["GO_MODULES_TOKEN"],
                                 "${{ github.event_name == 'push' && secrets.GO_MODULES_TOKEN || '' }}")
        source = next(step for step in steps if step.get("name") == "Checkout the pinned shared implementation")
        validation = next(step for step in steps if step.get("name") == "Local and CI pins agree")
        caller = next(step for step in steps if step.get("uses", "").startswith("actions/checkout@"))
        self.assertLess(steps.index(caller), steps.index(validation))
        self.assertLess(steps.index(validation), steps.index(source))
        self.assertEqual(source["with"]["repository"], "fin-assist/go-service-template")
        self.assertEqual(source["with"]["ref"], "${{ inputs.revision }}")

    def test_revision_service_and_local_pin_are_validated_before_source_checkout(self):
        title = "Validate immutable source and image profile"
        self.assertEqual(self.run_step(title).returncode, 0)
        for revision in ["main", "", "a" * 39, "a" * 41, "$(exit 0)"]:
            self.assertNotEqual(self.run_step(title, revision=revision).returncode, 0)
        for service in ["unknown", "service;true", "$(exit 0)"]:
            self.assertNotEqual(self.run_step(title, service=service).returncode, 0)
        self.assertNotEqual(self.run_step(title, service="user", fake="true").returncode, 0)
        self.assertEqual(self.run_step(title, service="assistant", fake="true").returncode, 0)
        self.assertEqual(self.run_step("Local and CI pins agree").returncode, 0)
        self.assertNotEqual(self.run_step("Local and CI pins agree", local_revision="b" * 40).returncode, 0)

    def test_tested_images_keep_stage_provenance_and_fake_provider_artifact(self):
        steps = self.workflow()["jobs"]["image"]["steps"]
        extract = next(step for step in steps if step.get("name") == "Extract stage binary from the tested image")
        self.assertEqual(extract["run"], 'python3 scripts/stage_artifact.py "$SERVICE" "$IMAGE_TAG" stage-artifact')
        uploads = [step for step in steps if step.get("uses", "").startswith("actions/upload-artifact@")]
        self.assertEqual(len(uploads), 2)
        for step in uploads:
            self.assertEqual(step["with"]["retention-days"], 30)
            self.assertEqual(step["with"]["if-no-files-found"], "error")
        self.assertEqual(uploads[0]["with"]["name"], "stage-${{ inputs.service }}-${{ github.sha }}-linux-amd64")
        self.assertEqual(uploads[1]["with"]["name"], "stage-fake-llm-${{ github.sha }}-linux-amd64")
        self.assertEqual(uploads[1]["if"], "inputs.fake_llm")


if __name__ == "__main__":
    unittest.main()
