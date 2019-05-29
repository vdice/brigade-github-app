const { events, Job } = require("brigadier");

const projectOrg = "brigadecore";
const projectName = "brigade-github-app";

const goImg = "quay.io/deis/lightweight-docker-go:v0.6.0";
const gopath = "/go";
const localPath = gopath + `/src/github.com/${projectOrg}/${projectName}`;

const releaseTagRegex = /^refs\/tags\/(v[0-9]+(?:\.[0-9]+)*(?:\-.+)?)$/;

function test() {
  // Create a new job to run Go tests
  var job = new Job("tests", goImg);
  job.mountPath = localPath;
  // Set a few environment variables.
  job.env = {
      "SKIP_DOCKER": "true"
  };
  // Run Go unit tests
  job.tasks = [
    `cd ${localPath}`,
    "make verify-vendored-code lint test"
  ];
  return job;
}

function buildAndPublishImages(project, version) {
  let dockerRegistry = project.secrets.dockerhubRegistry || "docker.io";
  let dockerOrg = project.secrets.dockerhubOrg || "brigadecore";
  var job = new Job("build-and-publish-images", "docker:stable-dind");
  job.privileged = true;
  job.tasks = [
    "apk add --update --no-cache make git",
    "dockerd-entrypoint.sh &",
    "sleep 20",
    "cd /src",
    `docker login ${dockerRegistry} -u ${project.secrets.dockerhubUsername} -p ${project.secrets.dockerhubPassword}`,
    `DOCKER_REGISTRY=${dockerRegistry} DOCKER_ORG=${dockerOrg} VERSION=${version} make build-all-images push-all-images`,
    `docker logout ${dockerRegistry}`
  ];
  return job;
}

// Here we can add additional Check Runs, which will run in parallel and
// report their results independently to GitHub
function runSuite(e, p) {
  // For the master branch, we build and publish images in response to the push
  // event. We test as a precondition for doing that, so we DON'T test here
  // for the master branch.
  if (e.revision.ref != "master") {
    // For now, this is the one-stop shop running build, lint and test targets
    runTests(e, p).catch(err => {console.error(err.toString())});
  }
}

// runTests is a Check Run that is run as part of a Checks Suite
function runTests(e, p) {
  console.log("Check requested");

  // Create Notification object (which is just a Job to update GH using the Checks API)
  var note = new Notification(`tests`, e, p);
  note.conclusion = "";
  note.title = "Run Tests";
  note.summary = "Running the test targets for " + e.revision.commit;
  note.text = "This test will ensure build, linting and tests all pass.";

  // Send notification, then run, then send pass/fail notification
  return notificationWrap(test(), note);
}

// A GitHub Check Suite notification
class Notification {
  constructor(name, e, p) {
    this.proj = p;
    this.payload = e.payload;
    this.name = name;
    this.externalID = e.buildID;
    this.detailsURL = `https://brigadecore.github.io/kashti/builds/${ e.buildID }`;
    this.title = "running check";
    this.text = "";
    this.summary = "";

    // count allows us to send the notification multiple times, with a distinct pod name
    // each time.
    this.count = 0;

    // One of: "success", "failure", "neutral", "cancelled", or "timed_out".
    this.conclusion = "neutral";
  }

  // Send a new notification, and return a Promise<result>.
  run() {
    this.count++;
    var job = new Job(`${ this.name }-notification-${ this.count }`, "brigadecore/brigade-github-check-run:latest");
    job.imageForcePull = true;
    job.env = {
      "CHECK_CONCLUSION": this.conclusion,
      "CHECK_NAME": this.name,
      "CHECK_TITLE": this.title,
      "CHECK_PAYLOAD": this.payload,
      "CHECK_SUMMARY": this.summary,
      "CHECK_TEXT": this.text,
      "CHECK_DETAILS_URL": this.detailsURL,
      "CHECK_EXTERNAL_ID": this.externalID
    };
    return job.run();
  }
}

// Helper to wrap a job execution between two notifications.
async function notificationWrap(job, note) {
  await note.run();
  try {
    let res = await job.run();
    const logs = await job.logs();
    note.conclusion = "success";
    note.summary = `Task "${ job.name }" passed`;
    note.text = note.text = "```" + res.toString() + "```\nTest Complete";
    return await note.run();
  } catch (e) {
    const logs = await job.logs();
    note.conclusion = "failure";
    note.summary = `Task "${ job.name }" failed for ${ e.buildID }`;
    note.text = "```" + logs + "```\nFailed with error: " + e.toString();
    try {
      return await note.run();
    } catch (e2) {
      console.error("failed to send notification: " + e2.toString());
      console.error("original error: " + e.toString());
      return e2;
    }
  }
}

events.on("exec", (e, p) => {
  return test(e, p).run();
})

events.on("push", (e, p) => {
  let matchStr = e.revision.ref.match(releaseTagRegex);
  if (matchStr) {
    // This is an official release with a semantically versioned tag
    let matchTokens = Array.from(matchStr);
    let version = matchTokens[1];
    buildAndPublishImages(p, version).run()
    .catch((err) => {
      console.error(err.toString());
    });
  }
  if (e.revision.ref == "refs/heads/master") {
    // This runs tests then builds and publishes "edge" images
    test().run()
    .then(() => {
      buildAndPublishImages(p, "").run();
    })
    .catch((err) => {
      console.error(err.toString());
    });
  }
})

events.on("check_suite:requested", runSuite);
events.on("check_suite:rerequested", runSuite);
events.on("check_run:rerequested", runSuite);
