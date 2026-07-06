import requests
import boto3
from botocore.exceptions import NoCredentialsError

def download_file(url, local_filename):
    """
    Downloads a file from the given URL and saves it locally.
    
    :param url: The URL of the file to download
    :param local_filename: The path to save the file locally
    :return: The local file path
    """
    response = requests.get(url, stream=True)
    if response.status_code == 200:
        with open(local_filename, 'wb') as f:
            for chunk in response.iter_content(1024):
                f.write(chunk)
        print(f"File downloaded successfully: {local_filename}")
        return local_filename
    else:
        raise Exception(f"Failed to download file: Status code {response.status_code}")
    